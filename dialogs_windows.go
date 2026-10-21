//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native Windows "Open" dialog (IFileOpenDialog), used for picking songs
// and folders. Must run on the UI thread.

var (
	ole32                = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoInitializeEx   = ole32.NewProc("CoInitializeEx")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")

	clsidFileOpenDialog = windows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = windows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
)

const (
	fosPickFolders     = 0x20
	fosForceFileSystem = 0x40
	fosAllowMulti      = 0x200
	fosPathMustExist   = 0x800
	fosFileMustExist   = 0x1000
	sigdnFileSysPath   = 0x80058000
	errCancelled       = 0x800704C7

	// IFileOpenDialog vtable slots
	vtRelease      = 2
	vtShow         = 3
	vtSetFileTypes = 4
	vtSetOptions   = 9
	vtGetOptions   = 10
	vtSetTitle     = 17
	vtGetResult    = 20
	vtGetResults   = 27
	// IShellItemArray
	vtArrGetCount  = 7
	vtArrGetItemAt = 8
	// IShellItem
	vtItemGetDisplayName = 5
)

type filterSpec struct{ name, spec string }

type comFilterSpec struct {
	name *uint16
	spec *uint16
}

func comCall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(unsafe.Pointer(obj))
	fn := *(*uintptr)(unsafe.Pointer(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{obj}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func fileDialog(owner uintptr, title string, folders, multi bool, filters []filterSpec) ([]string, error) {
	procCoInitializeEx.Call(0, 0x2) // COINIT_APARTMENTTHREADED (already initialised is fine)
	var dlg uintptr
	hr, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1, /*CLSCTX_INPROC_SERVER*/
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg)))
	if failed(hr) || dlg == 0 {
		return nil, fmt.Errorf("couldn't open the file dialog (0x%x)", uint32(hr))
	}
	defer comCall(dlg, vtRelease)

	var opts uint32
	comCall(dlg, vtGetOptions, uintptr(unsafe.Pointer(&opts)))
	opts |= fosForceFileSystem | fosPathMustExist
	if folders {
		opts |= fosPickFolders
	} else {
		opts |= fosFileMustExist
	}
	if multi {
		opts |= fosAllowMulti
	}
	comCall(dlg, vtSetOptions, uintptr(opts))
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		comCall(dlg, vtSetTitle, uintptr(unsafe.Pointer(t)))
	}
	if len(filters) > 0 {
		specs := make([]comFilterSpec, len(filters))
		for i, f := range filters {
			specs[i].name, _ = windows.UTF16PtrFromString(f.name)
			specs[i].spec, _ = windows.UTF16PtrFromString(f.spec)
		}
		comCall(dlg, vtSetFileTypes, uintptr(len(specs)), uintptr(unsafe.Pointer(&specs[0])))
	}
	hr = comCall(dlg, vtShow, owner)
	if uint32(hr) == errCancelled {
		return nil, nil
	}
	if failed(hr) {
		return nil, fmt.Errorf("file dialog failed (0x%x)", uint32(hr))
	}
	var arr uintptr
	hr = comCall(dlg, vtGetResults, uintptr(unsafe.Pointer(&arr)))
	if failed(hr) || arr == 0 {
		return nil, fmt.Errorf("couldn't read the selection (0x%x)", uint32(hr))
	}
	defer comCall(arr, vtRelease)
	var count uint32
	comCall(arr, vtArrGetCount, uintptr(unsafe.Pointer(&count)))
	var out []string
	for i := uint32(0); i < count; i++ {
		var item uintptr
		if failed(comCall(arr, vtArrGetItemAt, uintptr(i), uintptr(unsafe.Pointer(&item)))) || item == 0 {
			continue
		}
		var p *uint16
		if !failed(comCall(item, vtItemGetDisplayName, sigdnFileSysPath, uintptr(unsafe.Pointer(&p)))) && p != nil {
			out = append(out, windows.UTF16PtrToString(p))
			procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p)))
		}
		comCall(item, vtRelease)
	}
	return out, nil
}
