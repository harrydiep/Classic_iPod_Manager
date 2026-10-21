package edge

// PodManager addition: read the File objects attached to a web message
// (ICoreWebView2WebMessageReceivedEventArgs2::get_AdditionalObjects,
// WebView2 runtime 1.0.1774 and later).

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// FileDropMessagePrefix marks messages that carry dropped files.
const FileDropMessagePrefix = "podmanager-drop"

func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

const (
	slotRelease              = 2
	slotGetAdditionalObjects = 6 // after get_Source, get_WebMessageAsJson, TryGetWebMessageAsString
	slotCollectionCount      = 3
	slotCollectionValueAt    = 4
	slotFileGetPath          = 3
)

func additionalObjectPaths(args uintptr) []string {
	var coll uintptr
	if hr := comCall(args, slotGetAdditionalObjects, uintptr(unsafe.Pointer(&coll))); int32(hr) < 0 || coll == 0 {
		return nil
	}
	defer comCall(coll, slotRelease)
	var n uint32
	if hr := comCall(coll, slotCollectionCount, uintptr(unsafe.Pointer(&n))); int32(hr) < 0 {
		return nil
	}
	var paths []string
	for i := uint32(0); i < n; i++ {
		var obj uintptr
		if hr := comCall(coll, slotCollectionValueAt, uintptr(i), uintptr(unsafe.Pointer(&obj))); int32(hr) < 0 || obj == 0 {
			continue
		}
		var p *uint16
		if hr := comCall(obj, slotFileGetPath, uintptr(unsafe.Pointer(&p))); int32(hr) >= 0 && p != nil {
			paths = append(paths, windows.UTF16PtrToString(p))
			windows.CoTaskMemFree(unsafe.Pointer(p))
		}
		comCall(obj, slotRelease)
	}
	return paths
}
