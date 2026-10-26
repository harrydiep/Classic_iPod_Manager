//go:build windows

package main

import (
	"image"
	"image/color"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Media controls under the taskbar preview: hovering over Classic iPod
// Manager's taskbar button shows Previous, Play/Pause and Next buttons below
// the window's thumbnail, as media players do. They use the taskbar's
// ITaskbarList3 "thumbnail toolbar". The page tells us what's playing
// (playerState) and button clicks go back to the page's player.

// thumbState is what the page reports about playback.
type thumbState struct {
	Playing bool   `json:"playing"`
	CanPlay bool   `json:"canPlay"` // something to play
	CanPrev bool   `json:"canPrev"`
	CanNext bool   `json:"canNext"`
	Title   string `json:"title"` // the playing song, for the preview's tooltip
}

var (
	gdi32                  = windows.NewLazySystemDLL("gdi32.dll")
	procCreateDIBSection   = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap       = gdi32.NewProc("CreateBitmap")
	procDeleteObject       = gdi32.NewProc("DeleteObject")
	procCreateIconIndirect = user32.NewProc("CreateIconIndirect")
	procDestroyIcon        = user32.NewProc("DestroyIcon")
	procRegisterWindowMsg  = user32.NewProc("RegisterWindowMessageW")
	clsidTaskbarList       = windows.GUID{Data1: 0x56fdf344, Data2: 0xfd6d, Data3: 0x11d0, Data4: [8]byte{0x95, 0x8a, 0x00, 0x60, 0x97, 0xc9, 0xa0, 0x90}}
	iidITaskbarList3       = windows.GUID{Data1: 0xea1afb91, Data2: 0x9e28, Data3: 0x4b86, Data4: [8]byte{0x90, 0xe9, 0x9e, 0x9f, 0x8a, 0x5e, 0xee, 0x6f}}
)

// ITaskbarList3 methods (vtable slots)
const (
	tblRelease            = 2
	tblHrInit             = 3
	tblThumbBarAddButtons = 15
	tblThumbBarUpdate     = 16
	tblSetThumbnailTip    = 19
)

const (
	wmCommand       = 0x0111
	wmSettingChange = 0x001A
	wmDpiChanged    = 0x02E0
	thbnClicked     = 0x1800
	thbIcon         = 0x2
	thbTooltip      = 0x4
	thbFlags        = 0x8
	thbfEnabled     = 0x0
	thbfDisabled    = 0x1
	thumbPrev       = 1
	thumbPlay       = 2
	thumbNext       = 3
)

// THUMBBUTTON
type thumbButton struct {
	mask   uint32
	id     uint32
	bitmap uint32
	icon   uintptr
	tip    [260]uint16
	flags  uint32
}

type thumbBar struct {
	created uint32 // the "TaskbarButtonCreated" message
	hwnd    uintptr
	list    uintptr // ITaskbarList3
	added   bool
	state   thumbState
	icons   map[string]uintptr
	iconKey string // size and colour the icons were made for
	onClick func(cmd string)
}

func newThumbBar(onClick func(cmd string)) *thumbBar {
	tb := &thumbBar{onClick: onClick, icons: map[string]uintptr{}}
	name, _ := windows.UTF16PtrFromString("TaskbarButtonCreated")
	r, _, _ := procRegisterWindowMsg.Call(uintptr(unsafe.Pointer(name)))
	tb.created = uint32(r)
	return tb
}

// hook sees the window's messages (webview2.MessageHook); it runs on the
// window's thread.
func (tb *thumbBar) hook(hwnd, msg, wp, lp uintptr) (uintptr, bool) {
	switch {
	case tb.created != 0 && uint32(msg) == tb.created:
		// the taskbar button exists (again, if Explorer restarted)
		tb.hwnd = hwnd
		tb.install()
	case msg == wmCommand && hwnd == tb.hwnd && wp>>16&0xFFFF == thbnClicked:
		cmd := map[uintptr]string{thumbPrev: "prev", thumbPlay: "toggle", thumbNext: "next"}[wp&0xFFFF]
		if cmd != "" && tb.onClick != nil {
			go tb.onClick(cmd)
		}
		return 0, true
	case (msg == wmSettingChange || msg == wmDpiChanged) && hwnd == tb.hwnd && tb.added:
		// light/dark theme or screen scaling changed: redraw the icons
		tb.apply()
	}
	return 0, false
}

func (tb *thumbBar) call(slot int, args ...uintptr) uintptr {
	vtbl := *(*[24]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(tb.list))))
	r, _, _ := syscall.SyscallN(vtbl[slot], append([]uintptr{tb.list}, args...)...)
	return r
}

func (tb *thumbBar) install() {
	if tb.list != 0 {
		tb.call(tblRelease)
		tb.list = 0
	}
	tb.added = false
	procCoInitializeEx.Call(0, 2) // already done by WebView2; harmless
	var list uintptr
	const clsctxInprocServer = 1
	if hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidTaskbarList)), 0, clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidITaskbarList3)), uintptr(unsafe.Pointer(&list))); hr != 0 || list == 0 {
		return
	}
	tb.list = list
	if tb.call(tblHrInit) != 0 {
		tb.call(tblRelease)
		tb.list = 0
		return
	}
	btns := tb.buttons()
	if tb.call(tblThumbBarAddButtons, tb.hwnd, uintptr(len(btns)), uintptr(unsafe.Pointer(&btns[0]))) == 0 {
		tb.added = true
		tb.setTooltip()
	}
}

// update shows the page's playback state on the buttons.
func (tb *thumbBar) update(st thumbState) {
	tb.state = st
	tb.apply()
}

func (tb *thumbBar) apply() {
	if !tb.added {
		return
	}
	btns := tb.buttons()
	tb.call(tblThumbBarUpdate, tb.hwnd, uintptr(len(btns)), uintptr(unsafe.Pointer(&btns[0])))
	tb.setTooltip()
}

func (tb *thumbBar) setTooltip() {
	// the preview's tooltip names the playing song; nil restores the window title
	if tb.state.Title == "" {
		tb.call(tblSetThumbnailTip, tb.hwnd, 0)
		return
	}
	t, err := windows.UTF16PtrFromString(tb.state.Title)
	if err != nil {
		return
	}
	tb.call(tblSetThumbnailTip, tb.hwnd, uintptr(unsafe.Pointer(t)))
	runtime.KeepAlive(t)
}

func (tb *thumbBar) buttons() []thumbButton {
	tb.makeIcons()
	st := tb.state
	mk := func(id uint32, icon, tip string, enabled bool) thumbButton {
		b := thumbButton{mask: thbIcon | thbTooltip | thbFlags, id: id, icon: tb.icons[icon], flags: thbfEnabled}
		if !enabled {
			b.flags = thbfDisabled
		}
		t, _ := windows.UTF16FromString(tip)
		copy(b.tip[:len(b.tip)-1], t)
		return b
	}
	playIcon, playTip := "play", "Play"
	if st.Playing {
		playIcon, playTip = "pause", "Pause"
	}
	return []thumbButton{
		mk(thumbPrev, "prev", "Previous", st.CanPrev),
		mk(thumbPlay, playIcon, playTip, st.CanPlay || st.Playing),
		mk(thumbNext, "next", "Next", st.CanNext),
	}
}

// makeIcons draws the four glyphs at the small-icon size for the window's
// screen, white on a dark taskbar or near-black on a light one.
func (tb *thumbBar) makeIcons() {
	size := 16
	if procGetDpiForWindow.Find() == nil && procGetSystemMetricsForDpi.Find() == nil {
		if dpi, _, _ := procGetDpiForWindow.Call(tb.hwnd); dpi > 0 {
			const smCxSmIcon = 49
			if v, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, dpi); v > 0 {
				size = int(v)
			}
		}
	}
	c := color.NRGBA{255, 255, 255, 255}
	if systemUsesLightTheme() {
		c = color.NRGBA{32, 32, 32, 255}
	}
	key := string(rune(size)) + string(rune(c.R))
	if key == tb.iconKey {
		return
	}
	for k, h := range tb.icons {
		procDestroyIcon.Call(h)
		delete(tb.icons, k)
	}
	for _, k := range []string{"prev", "play", "pause", "next"} {
		if h := iconFromImage(thumbGlyph(k, size, c)); h != 0 {
			tb.icons[k] = h
		}
	}
	tb.iconKey = key
}

// systemUsesLightTheme reports whether the taskbar uses Windows' light theme.
func systemUsesLightTheme() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SystemUsesLightTheme")
	return err == nil && v == 1
}

type bitmapInfoHeader struct {
	size          uint32
	width         int32
	height        int32
	planes        uint16
	bitCount      uint16
	compression   uint32
	sizeImage     uint32
	xPelsPerMeter int32
	yPelsPerMeter int32
	clrUsed       uint32
	clrImportant  uint32
}

type iconInfo struct {
	fIcon    int32
	xHotspot uint32
	yHotspot uint32
	hbmMask  uintptr
	hbmColor uintptr
}

// iconFromImage makes an HICON with an alpha channel from an image.
func iconFromImage(img *image.NRGBA) uintptr {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	bi := bitmapInfoHeader{size: uint32(unsafe.Sizeof(bitmapInfoHeader{})), width: int32(w), height: -int32(h), planes: 1, bitCount: 32}
	var bits unsafe.Pointer
	hbmColor, _, _ := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&bi)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if hbmColor == 0 || bits == nil {
		return 0
	}
	defer procDeleteObject.Call(hbmColor)
	px := unsafe.Slice((*byte)(bits), w*h*4)
	for i := 0; i < w*h; i++ {
		s := img.Pix[i*4 : i*4+4]
		px[i*4], px[i*4+1], px[i*4+2], px[i*4+3] = s[2], s[1], s[0], s[3] // BGRA
	}
	mask, _, _ := procCreateBitmap.Call(uintptr(w), uintptr(h), 1, 1, 0)
	if mask == 0 {
		return 0
	}
	defer procDeleteObject.Call(mask)
	ii := iconInfo{fIcon: 1, hbmMask: mask, hbmColor: hbmColor}
	hicon, _, _ := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	return hicon
}
