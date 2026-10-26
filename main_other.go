//go:build !windows

// On non-Windows systems Classic iPod Manager runs as a small development server so
// the interface and backend can be exercised in a browser:
//
//	go run . -roots /path/to/mounted/ipod -pick /path/to/music
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"podmanager/internal/app"
	"podmanager/internal/iap"
	"podmanager/internal/media"
)

type devPlatform struct {
	roots     []string
	pick      string
	exportDir string
	battery   string
	image     string
}

func (p *devPlatform) Roots() []string { return p.roots }

func (p *devPlatform) VolumeInfo(root string) (string, uint64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return "", 0, 0, err
	}
	return filepath.Base(root), st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}

func (p *devPlatform) Eject(root string) error {
	log.Printf("eject %s (simulated)", root)
	return nil
}

func (p *devPlatform) PickFiles() ([]string, error) {
	if p.pick == "" {
		return nil, errors.New("start with -pick <folder> to simulate the file dialog")
	}
	var out []string
	entries, err := os.ReadDir(p.pick)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() && media.KnownAudioExt(e.Name()) {
			out = append(out, filepath.Join(p.pick, e.Name()))
		}
	}
	return out, nil
}

func (p *devPlatform) PickFolder(title string) (string, error) {
	if strings.Contains(title, "copy") {
		return p.exportDir, nil
	}
	return p.pick, nil
}

func (p *devPlatform) DragOut(files []app.DragFile) error {
	for _, f := range files {
		log.Printf("drag out %s as %q (%d bytes)", f.Path, f.Name, f.Size)
	}
	return nil
}

func (p *devPlatform) Battery(serial string) (iap.Battery, error) {
	// -battery "72,charging" simulates an iPod that reports its battery.
	if p.battery == "" {
		return iap.Battery{}, app.ErrNoAccessoryPort
	}
	pct, state, _ := strings.Cut(p.battery, ",")
	var n int
	fmt.Sscan(pct, &n)
	b := iap.Battery{State: iap.StateBattery, Level: byte(n * 255 / 100)}
	switch state {
	case "charging":
		b.State = iap.StateCharging
	case "charged":
		b.State = iap.StateCharged
	case "low":
		b.State = iap.StateBatteryLow
	}
	log.Printf("battery query for serial %q: %+v", serial, b)
	return b, nil
}

func (p *devPlatform) BatteryReport(serial string) (iap.Battery, string, error) {
	b, err := p.Battery(serial)
	r := fmt.Sprintf("Classic iPod Manager %s battery report (development server)\niPod FireWire GUID: %s\n\nSimulated with -battery %q.\n", app.Version, serial, p.battery)
	if err != nil {
		r += "\nResult: failed: " + err.Error() + "\n"
	} else {
		r += fmt.Sprintf("\nResult: %s, level %d/255 (%d%%)\n", b.Describe(), b.Level, b.Percent())
	}
	return b, r, err
}

func (p *devPlatform) PickAny(title string, filters []app.FileFilter, multi bool) ([]string, error) {
	// Pictures come from -image; songs from the -pick folder (every format).
	if len(filters) > 0 && strings.Contains(filters[0].Pattern, "*.jpg") {
		if p.image == "" {
			return nil, errors.New("start with -image <file> to simulate choosing a picture")
		}
		return []string{p.image}, nil
	}
	if p.pick == "" {
		return nil, errors.New("start with -pick <folder> to simulate the file dialog")
	}
	videos := len(filters) > 0 && strings.Contains(filters[0].Pattern, "*.m4v")
	var out []string
	entries, _ := os.ReadDir(p.pick)
	for _, e := range entries {
		path := filepath.Join(p.pick, e.Name())
		if e.IsDir() {
			continue
		}
		isVid := media.VideoExt(path) && media.IsVideo(path)
		if (videos && isVid) || (!videos && !isVid && media.DecodableExt(path)) {
			out = append(out, path)
		}
	}
	return out, nil
}

func (p *devPlatform) OpenFolder(path string) error {
	log.Printf("open folder %s", path)
	return nil
}

func (p *devPlatform) Reveal(path string) error {
	log.Printf("reveal %s", path)
	return nil
}

type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
}

func (h *hub) publish(b []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.subs {
		select {
		case c <- b:
		default:
		}
	}
}

const devBridge = `<script>
(function(){
  var pending = {}, seq = 0;
  window.__podReply = function(id, ok, payload){ var p = pending[id]; if(!p) return; delete pending[id]; ok ? p.resolve(payload) : p.reject(new Error(payload)); };
  window.pm_call = function(id, method, args){
    fetch('/rpc', {method:'POST', body: JSON.stringify({id:id, method:method, args:args})})
      .then(function(r){ return r.json(); })
      .then(function(r){ window.__podReply(id, r.ok, r.ok ? r.result : r.error); })
      .catch(function(e){ window.__podReply(id, false, String(e)); });
  };
  window.podCall = function(method){ var args = Array.prototype.slice.call(arguments, 1);
    return new Promise(function(res, rej){ var id = ++seq; pending[id] = {resolve:res, reject:rej}; window.pm_call(id, method, args); }); };
  var es = new EventSource('/events');
  es.onmessage = function(m){ window.__podEvent && window.__podEvent(JSON.parse(m.data)); };
  window.__podPlatform = 'dev';
})();
</script>`

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "listen address")
	roots := flag.String("roots", "", "comma separated folders that act as connected iPods")
	pick := flag.String("pick", "", "folder whose songs are returned by the simulated file dialog")
	export := flag.String("export", os.TempDir(), "folder used by the simulated export dialog")
	backups := flag.String("backups", filepath.Join(os.TempDir(), "podmanager-backups"), "backup folder")
	battery := flag.String("battery", "", `simulated battery, e.g. "72,charging" (empty: not reported)`)
	image := flag.String("image", "", "picture returned by the simulated picture dialog")
	mainDir := flag.String("main", filepath.Join(os.TempDir(), "ClassiciPodManager-main"), "Classic iPod Manager's main folder (encoders, converted songs)")
	flag.Parse()

	plat := &devPlatform{pick: *pick, exportDir: *export, battery: *battery, image: *image}
	for _, r := range strings.Split(*roots, ",") {
		if r = strings.TrimSpace(r); r != "" {
			plat.roots = append(plat.roots, r)
		}
	}
	h := &hub{subs: map[chan []byte]bool{}}
	a := app.New(plat, *backups, func(e app.Event) {
		b, _ := json.Marshal(e)
		h.publish(b)
	})
	a.SetPrefsFile(filepath.Join(*backups, "settings.json"))
	a.SetMainFolder(*mainDir)
	a.ClearPlaybackCache()
	a.SetMediaBase("/")
	http.Handle("/media/", a.MediaHandler())
	methods := a.Methods()
	// Browsers don't reveal dropped files' paths, so in dev mode dropped
	// names are looked up in the -pick folder.
	methods["playerState"] = func(st map[string]any) {} // taskbar preview buttons exist only on Windows
	methods["dropNames"] = func(names []string) {
		var paths []string
		for _, n := range names {
			paths = append(paths, filepath.Join(plat.pick, filepath.Base(n)))
		}
		go a.HandleDrop(paths)
	}
	stop := make(chan struct{})
	go a.Watch(stop)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.Replace(indexHTML, "<head>", "<head>"+devBridge, 1)
		fmt.Fprint(w, html)
	})
	http.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     int               `json:"id"`
			Method string            `json:"method"`
			Args   []json.RawMessage `json:"args"`
		}
		resp := map[string]interface{}{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		} else if fn, ok := methods[req.Method]; !ok {
			resp["ok"], resp["error"] = false, "unknown method "+req.Method
		} else if res, err := app.Invoke(fn, req.Args); err != nil {
			resp["ok"], resp["error"] = false, err.Error()
		} else {
			resp["ok"], resp["result"] = true, res
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no streaming", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		c := make(chan []byte, 256)
		h.mu.Lock()
		h.subs[c] = true
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			delete(h.subs, c)
			h.mu.Unlock()
		}()
		fmt.Fprint(w, ": hi\n\n")
		fl.Flush()
		for {
			select {
			case b := <-c:
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	log.Printf("Classic iPod Manager dev server on http://%s", *addr)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
