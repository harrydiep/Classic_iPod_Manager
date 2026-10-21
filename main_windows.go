//go:build windows

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	webview2 "github.com/jchv/go-webview2"

	"podmanager/internal/app"
)

func main() {
	runtime.LockOSThread()

	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		local = os.TempDir()
	}
	roaming := os.Getenv("APPDATA")
	if roaming == "" {
		roaming = local
	}
	dataDir := filepath.Join(local, "PodManager")
	backupDir := filepath.Join(roaming, "PodManager", "Backups")
	os.MkdirAll(dataDir, 0o755)

	plat := &winPlatform{}
	var w webview2.WebView
	ready := make(chan struct{})
	evalJS := func(js string) {
		<-ready
		w.Dispatch(func() { w.Eval(js) })
	}
	a := app.New(plat, backupDir, func(e app.Event) {
		b, err := json.Marshal(e)
		if err == nil {
			evalJS("window.__podEvent&&window.__podEvent(" + string(b) + ")")
		}
	})

	w = webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     os.Getenv("PODMANAGER_DEBUG") != "",
		AutoFocus: true,
		DataPath:  filepath.Join(dataDir, "WebView2"),
		WindowOptions: webview2.WindowOptions{
			Title:  "PodManager",
			Width:  1200,
			Height: 780,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		messageBox("PodManager",
			"PodManager needs the Microsoft Edge WebView2 Runtime, which is included with Windows 10 and 11.\n\n"+
				"Please install it from https://go.microsoft.com/fwlink/p/?LinkId=2124703 and start PodManager again.", 0x10)
		return
	}
	defer w.Destroy()
	a.SetPrefsFile(filepath.Join(dataDir, "settings.json"))
	configureBrowser(w)
	// Files dropped from Explorer arrive with their paths (see the patched
	// go-webview2 in third_party); check them and let the page confirm.
	if c := chromiumOf(w); c != nil {
		c.FileDropCallback = func(_ string, paths []string) { go a.HandleDrop(paths) }
	}
	if sc := a.SavedScale(); sc != 1 {
		setZoom(w, sc)
	}
	a.SetZoomFunc(func(f float64) {
		done := make(chan struct{})
		w.Dispatch(func() { setZoom(w, f); close(done) })
		<-done
	})
	plat.hwnd = uintptr(w.Window())
	plat.dispatch = w.Dispatch
	w.SetSize(940, 600, webview2.HintMin)

	methods := a.Methods()
	// All calls run off the UI thread; results come back through __podReply.
	w.Bind("pm_call", func(id int, method string, args []json.RawMessage) {
		go func() {
			var res json.RawMessage
			var err error
			func() {
				defer func() {
					if r := recover(); r != nil {
						err = fmt.Errorf("internal error: %v", r)
					}
				}()
				fn, ok := methods[method]
				if !ok {
					err = fmt.Errorf("unknown method %q", method)
					return
				}
				res, err = app.Invoke(fn, args)
			}()
			var js string
			if err != nil {
				msg, _ := json.Marshal(err.Error())
				js = fmt.Sprintf("window.__podReply(%d,false,%s)", id, msg)
			} else {
				if len(res) == 0 {
					res = json.RawMessage("null")
				}
				js = fmt.Sprintf("window.__podReply(%d,true,%s)", id, res)
			}
			evalJS(js)
		}()
	})
	w.Init(bridgeJS)
	w.SetHtml(indexHTML)
	close(ready)

	stop := make(chan struct{})
	go a.Watch(stop)
	w.Run()
	close(stop)
}

const bridgeJS = `
(function(){
  var pending = {}, seq = 0;
  window.__podReply = function(id, ok, payload){
    var p = pending[id]; if (!p) return; delete pending[id];
    if (ok) p.resolve(payload); else p.reject(new Error(payload));
  };
  window.podCall = function(method){
    var args = Array.prototype.slice.call(arguments, 1);
    return new Promise(function(resolve, reject){
      var id = ++seq; pending[id] = {resolve: resolve, reject: reject};
      window.pm_call(id, method, args);
    });
  };
  window.__podPlatform = 'windows';
})();
`
