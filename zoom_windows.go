//go:build windows

package main

import (
	"math"
	"reflect"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/jchv/go-webview2/pkg/edge"
	"golang.org/x/sys/windows"
)

// go-webview2 doesn't expose the underlying WebView2 objects, so reach the
// (unexported) browser field to set the zoom factor and browser settings.
func chromiumOf(w webview2.WebView) *edge.Chromium {
	v := reflect.ValueOf(w)
	if v.Kind() != reflect.Ptr || v.Elem().Kind() != reflect.Struct {
		return nil
	}
	f := v.Elem().FieldByName("browser")
	if !f.IsValid() {
		return nil
	}
	f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	c, _ := f.Interface().(*edge.Chromium)
	return c
}

// configureBrowser turns off browser behaviour that doesn't belong in a
// desktop app: Ctrl+wheel zoom (the app has its own setting), browser
// shortcuts such as F5/Ctrl+R that would reload the page, and swipe
// navigation.
func configureBrowser(w webview2.WebView) {
	c := chromiumOf(w)
	if c == nil {
		return
	}
	s, err := c.GetSettings()
	if err != nil || s == nil {
		return
	}
	s.PutIsZoomControlEnabled(false)
	s.PutIsStatusBarEnabled(false)
	// Newer settings only exist on newer runtimes; check before calling.
	if supports(uintptr(unsafe.Pointer(s)), iidSettings3) {
		s.PutAreBrowserAcceleratorKeysEnabled(false)
	}
	if supports(uintptr(unsafe.Pointer(s)), iidSettings6) {
		s.PutIsSwipeNavigationEnabled(false)
	}
}

var (
	iidSettings3 = windows.GUID{Data1: 0xfdb5ab74, Data2: 0xaf33, Data3: 0x4854, Data4: [8]byte{0x84, 0xf0, 0x0a, 0x63, 0x1d, 0xeb, 0x5e, 0xba}}
	iidSettings6 = windows.GUID{Data1: 0x11cb3acd, Data2: 0x9bc8, Data3: 0x43b8, Data4: [8]byte{0x83, 0xbf, 0xf4, 0x07, 0x53, 0x71, 0x4f, 0x87}}
)

// supports asks a COM object whether it implements an interface.
func supports(obj uintptr, iid windows.GUID) bool {
	var out uintptr
	hr := comCall(obj, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&out)))
	if failed(hr) || out == 0 {
		return false
	}
	comCall(out, vtRelease)
	return true
}

const vtPutZoomFactor = 8 // ICoreWebView2Controller::put_ZoomFactor

// setZoom sets the WebView2 zoom factor. Must run on the UI thread.
func setZoom(w webview2.WebView, factor float64) {
	c := chromiumOf(w)
	if c == nil {
		return
	}
	ctrl := c.GetController()
	if ctrl == nil {
		return
	}
	// The double argument is passed in XMM1; Go's stdcall trampoline loads
	// the first four arguments into both integer and XMM registers.
	comCall(uintptr(unsafe.Pointer(ctrl)), vtPutZoomFactor, uintptr(math.Float64bits(factor)))
}
