//go:build windows

package main

import (
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// How Windows names the program and draws its icon: the taskbar button,
// Alt+Tab, the title bar, Task Manager and pinned taskbar shortcuts.
//
//   - The exe's version information (winres/winres.json) gives Task Manager
//     and Explorer the name "Classic iPod Manager".
//   - An explicit AppUserModelID makes the taskbar treat the program as its
//     own app instead of grouping it by whatever the exe file is called, and
//     the window's relaunch properties give a pinned button the program's
//     name and icon.
//   - The window gets the program's icon at the right size for the screen's
//     scaling, both the large one (Alt+Tab, taskbar) and the small one
//     (title bar).

const appUserModelID = "ClassiciPodManager.App"

var (
	procSetAppID                 = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procSHGetPropertyStoreForWnd = shell32.NewProc("SHGetPropertyStoreForWindow")
	procLoadImageW               = user32.NewProc("LoadImageW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi   = user32.NewProc("GetSystemMetricsForDpi")
)

// setProcessIdentity runs before any window is created.
func setProcessIdentity() {
	if procSetAppID.Find() != nil {
		return
	}
	id, _ := windows.UTF16PtrFromString(appUserModelID)
	procSetAppID.Call(uintptr(unsafe.Pointer(id)))
}

// setWindowIdentity gives the main window its icons and taskbar identity.
func setWindowIdentity(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	setWindowIcons(hwnd)
	setRelaunchProperties(hwnd)
}

func setWindowIcons(hwnd uintptr) {
	const (
		imageIcon    = 1
		lrShared     = 0x8000
		wmSetIcon    = 0x0080
		iconSmall    = 0
		iconBig      = 1
		smCxIcon     = 11
		smCxSmIcon   = 49
		resourceIcon = 1 // RT_GROUP_ICON #1 in winres.json
	)
	var hinst windows.Handle
	if windows.GetModuleHandleEx(0, nil, &hinst) != nil {
		return
	}
	metric := func(index uintptr, fallback int) int {
		if procGetDpiForWindow.Find() == nil && procGetSystemMetricsForDpi.Find() == nil {
			dpi, _, _ := procGetDpiForWindow.Call(hwnd)
			if dpi > 0 {
				if v, _, _ := procGetSystemMetricsForDpi.Call(index, dpi); v > 0 {
					return int(v)
				}
			}
		}
		if v, _, _ := procGetSystemMetrics.Call(index); v > 0 {
			return int(v)
		}
		return fallback
	}
	load := func(size int) uintptr {
		// not LR_SHARED: each size is its own image, picked from the icon's
		// 16/32/48/64/256 pixel versions
		h, _, _ := procLoadImageW.Call(uintptr(hinst), resourceIcon, imageIcon, uintptr(size), uintptr(size), 0)
		return h
	}
	if big := load(metric(smCxIcon, 32)); big != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconBig, big)
	}
	if small := load(metric(smCxSmIcon, 16)); small != 0 {
		procSendMessageW.Call(hwnd, wmSetIcon, iconSmall, small)
	}
}

// IPropertyStore, for the window's taskbar properties.
type propertyKey struct {
	fmtid windows.GUID
	pid   uint32
}

type propVariant struct {
	vt       uint16
	reserved [3]uint16
	val      uintptr
	pad      uintptr
}

var (
	iidIPropertyStore = windows.GUID{Data1: 0x886d8eeb, Data2: 0x8cf2, Data3: 0x4446, Data4: [8]byte{0x8d, 0x02, 0xcd, 0xba, 0x1d, 0xbd, 0xcf, 0x99}}
	appUserModelProps = windows.GUID{Data1: 0x9f4c2855, Data2: 0x9f79, Data3: 0x4b39, Data4: [8]byte{0xa8, 0xd0, 0xe1, 0xd4, 0x2d, 0xe1, 0xd5, 0xf3}}
)

// setRelaunchProperties tells the taskbar what a pinned button should run
// and how to show it: the program's name and icon rather than the exe's.
func setRelaunchProperties(hwnd uintptr) {
	if procSHGetPropertyStoreForWnd.Find() != nil {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	var store uintptr
	if hr, _, _ := procSHGetPropertyStoreForWnd.Call(hwnd, uintptr(unsafe.Pointer(&iidIPropertyStore)), uintptr(unsafe.Pointer(&store))); hr != 0 || store == 0 {
		return
	}
	vtbl := *(*[8]uintptr)(unsafe.Pointer(*(*uintptr)(unsafe.Pointer(store))))
	const (
		release  = 2
		setValue = 6
		commit   = 7
	)
	set := func(pid uint32, value string) {
		s, err := windows.UTF16PtrFromString(value)
		if err != nil {
			return
		}
		key := propertyKey{appUserModelProps, pid}
		pv := propVariant{vt: 31 /* VT_LPWSTR */, val: uintptr(unsafe.Pointer(s))}
		syscall.SyscallN(vtbl[setValue], store, uintptr(unsafe.Pointer(&key)), uintptr(unsafe.Pointer(&pv)))
		runtime.KeepAlive(s)
	}
	// PKEY_AppUserModel_ID must be set for the relaunch properties to count.
	set(5, appUserModelID)
	set(2, `"`+exe+`"`) // RelaunchCommand
	set(4, appName)     // RelaunchDisplayNameResource
	set(3, exe+",-1")   // RelaunchIconResource: icon resource #1
	syscall.SyscallN(vtbl[commit], store)
	syscall.SyscallN(vtbl[release], store)
}
