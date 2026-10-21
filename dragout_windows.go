//go:build windows

package main

// Dragging songs out of PodManager into Explorer.
//
// The songs are offered as "virtual files" (FileGroupDescriptorW +
// FileContents): Explorer names them "Artist - Title.mp3" and reads each one
// through a stream opened on the file on the iPod, so nothing is staged on
// disk first. The data object also supports asynchronous transfers, so the
// copy runs on Explorer's side with its own progress window while
// PodManager stays responsive.

import (
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"podmanager/internal/app"
)

var (
	procOleInitialize          = ole32.NewProc("OleInitialize")
	procDoDragDrop             = ole32.NewProc("DoDragDrop")
	shell32                    = windows.NewLazySystemDLL("shell32.dll")
	procSHCreateStdEnumFmtEtc  = shell32.NewProc("SHCreateStdEnumFmtEtc")
	shlwapi                    = windows.NewLazySystemDLL("shlwapi.dll")
	procSHCreateStreamOnFileEx = shlwapi.NewProc("SHCreateStreamOnFileEx")
	procRegisterClipboardFmt   = user32.NewProc("RegisterClipboardFormatW")
	procGetAsyncKeyState       = user32.NewProc("GetAsyncKeyState")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procGlobalAlloc            = kernel32.NewProc("GlobalAlloc")
	procGlobalLock             = kernel32.NewProc("GlobalLock")
	procGlobalUnlock           = kernel32.NewProc("GlobalUnlock")
)

const (
	sOK                    = 0
	sFalse                 = 1
	eNotImpl               = 0x80004001
	eNoInterface           = 0x80004002
	eOutOfMemory           = 0x8007000E
	dvEFormatEtc           = 0x80040064
	oleEAdviseNotSupported = 0x80040003
	dataSSameFormatEtc     = 0x00040130
	dragdropSDrop          = 0x00040100
	dragdropSCancel        = 0x00040101
	dragdropSUseDefCursors = 0x00040102
	tymedHGlobal           = 1
	tymedIStream           = 4
	dropEffectCopy         = 1
	fdAttributes           = 0x4
	fdFileSize             = 0x40
	fdProgressUI           = 0x4000
	fileAttributeNormal    = 0x80
	gmemMoveableZero       = 0x0042
	stgmReadShareDenyWrite = 0x00000020
	fileDescriptorSize     = 592
	vkLButton, vkRButton   = 0x01, 0x02
)

var (
	iidIUnknown        = windows.GUID{Data1: 0x00000000, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDataObject     = windows.GUID{Data1: 0x0000010e, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDropSource     = windows.GUID{Data1: 0x00000121, Data2: 0, Data3: 0, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidAsyncCapability = windows.GUID{Data1: 0x3D8B0590, Data2: 0xF691, Data3: 0x11d2, Data4: [8]byte{0x8E, 0xA9, 0x00, 0x60, 0x97, 0xDF, 0x5B, 0xD4}}
	cfFileGroupDescW   uint16
	cfFileContents     uint16
	cfPreferredEffect  uint16
	formatsOnce        sync.Once
)

// FORMATETC (64-bit layout)
type formatEtc struct {
	cfFormat uint16
	_        [6]byte
	ptd      uintptr
	aspect   uint32
	lindex   int32
	tymed    uint32
	_        uint32
}

// STGMEDIUM (64-bit layout)
type stgMedium struct {
	tymed   uint32
	_       uint32
	handle  uintptr // HGLOBAL or IStream*
	release uintptr
}

func registerFormats() {
	formatsOnce.Do(func() {
		reg := func(n string) uint16 {
			p, _ := windows.UTF16PtrFromString(n)
			r, _, _ := procRegisterClipboardFmt.Call(uintptr(unsafe.Pointer(p)))
			return uint16(r)
		}
		cfFileGroupDescW = reg("FileGroupDescriptorW")
		cfFileContents = reg("FileContents")
		cfPreferredEffect = reg("Preferred DropEffect")
	})
}

// ---------------------------------------------------------------- objects

// dataObject implements IDataObject and IDataObjectAsyncCapability. Both
// interface pointers live inside the struct; COM keeps them, so live
// objects are pinned in the registry until their last Release.
type dataObject struct {
	vtbl      *[12]uintptr // IDataObject
	asyncVtbl *[8]uintptr  // IDataObjectAsyncCapability
	refs      int32
	files     []app.DragFile
	mu        sync.Mutex
	async     bool
	inOp      bool
}

type dropSource struct {
	vtbl *[5]uintptr
	refs int32
	btn  uintptr // mouse button that started the drag
}

var (
	liveMu      sync.Mutex
	liveObjects = map[uintptr]interface{}{}
	dataVtbl    [12]uintptr
	asyncVtbl   [8]uintptr
	sourceVtbl  [5]uintptr
	vtblsOnce   sync.Once
)

func dataFromThis(this uintptr) *dataObject {
	liveMu.Lock()
	defer liveMu.Unlock()
	o, _ := liveObjects[this].(*dataObject)
	return o
}

func dataFromAsync(this uintptr) *dataObject {
	return dataFromThis(this - unsafe.Sizeof(uintptr(0)))
}

func guidAt(p uintptr) windows.GUID { return *(*windows.GUID)(unsafe.Pointer(p)) }

func setOut(pp uintptr, v uintptr) {
	if pp != 0 {
		*(*uintptr)(unsafe.Pointer(pp)) = v
	}
}

func buildVtables() {
	vtblsOnce.Do(func() {
		// --- IDataObject
		dataVtbl[0] = syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			o := dataFromThis(this)
			if o == nil {
				return eNoInterface
			}
			switch guidAt(riid) {
			case iidIUnknown, iidIDataObject:
				setOut(ppv, this)
			case iidAsyncCapability:
				setOut(ppv, this+unsafe.Sizeof(uintptr(0)))
			default:
				setOut(ppv, 0)
				return eNoInterface
			}
			o.addRef()
			return sOK
		})
		dataVtbl[1] = syscall.NewCallback(func(this uintptr) uintptr {
			if o := dataFromThis(this); o != nil {
				return uintptr(o.addRef())
			}
			return 0
		})
		dataVtbl[2] = syscall.NewCallback(func(this uintptr) uintptr {
			if o := dataFromThis(this); o != nil {
				return uintptr(o.release(this))
			}
			return 0
		})
		dataVtbl[3] = syscall.NewCallback(func(this, pFmt, pMed uintptr) uintptr { // GetData
			o := dataFromThis(this)
			if o == nil || pFmt == 0 || pMed == 0 {
				return dvEFormatEtc
			}
			return o.getData((*formatEtc)(unsafe.Pointer(pFmt)), (*stgMedium)(unsafe.Pointer(pMed)))
		})
		dataVtbl[4] = syscall.NewCallback(func(this, pFmt, pMed uintptr) uintptr { return eNotImpl }) // GetDataHere
		dataVtbl[5] = syscall.NewCallback(func(this, pFmt uintptr) uintptr {                          // QueryGetData
			o := dataFromThis(this)
			if o == nil || pFmt == 0 {
				return dvEFormatEtc
			}
			return o.queryGetData((*formatEtc)(unsafe.Pointer(pFmt)))
		})
		dataVtbl[6] = syscall.NewCallback(func(this, pIn, pOut uintptr) uintptr { // GetCanonicalFormatEtc
			if pOut != 0 {
				(*formatEtc)(unsafe.Pointer(pOut)).ptd = 0
			}
			return dataSSameFormatEtc
		})
		dataVtbl[7] = syscall.NewCallback(func(this, pFmt, pMed, release uintptr) uintptr { // SetData
			return eNotImpl // the caller keeps ownership of the medium
		})
		dataVtbl[8] = syscall.NewCallback(func(this, dir, ppEnum uintptr) uintptr { // EnumFormatEtc
			if dir != 1 { // DATADIR_GET
				return eNotImpl
			}
			registerFormats()
			fmts := []formatEtc{
				{cfFormat: cfFileGroupDescW, aspect: 1, lindex: -1, tymed: tymedHGlobal},
				{cfFormat: cfFileContents, aspect: 1, lindex: -1, tymed: tymedIStream},
				{cfFormat: cfPreferredEffect, aspect: 1, lindex: -1, tymed: tymedHGlobal},
			}
			hr, _, _ := procSHCreateStdEnumFmtEtc.Call(uintptr(len(fmts)), uintptr(unsafe.Pointer(&fmts[0])), ppEnum)
			return hr
		})
		dataVtbl[9] = syscall.NewCallback(func(this, a, b, c, d uintptr) uintptr { return oleEAdviseNotSupported })
		dataVtbl[10] = syscall.NewCallback(func(this, a uintptr) uintptr { return oleEAdviseNotSupported })
		dataVtbl[11] = syscall.NewCallback(func(this, a uintptr) uintptr { return oleEAdviseNotSupported })

		// --- IDataObjectAsyncCapability (this = &obj.asyncVtbl)
		asyncVtbl[0] = syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			return comCall(this-unsafe.Sizeof(uintptr(0)), 0, riid, ppv)
		})
		asyncVtbl[1] = syscall.NewCallback(func(this uintptr) uintptr {
			if o := dataFromAsync(this); o != nil {
				return uintptr(o.addRef())
			}
			return 0
		})
		asyncVtbl[2] = syscall.NewCallback(func(this uintptr) uintptr {
			if o := dataFromAsync(this); o != nil {
				return uintptr(o.release(this - unsafe.Sizeof(uintptr(0))))
			}
			return 0
		})
		asyncVtbl[3] = syscall.NewCallback(func(this, on uintptr) uintptr { // SetAsyncMode
			if o := dataFromAsync(this); o != nil {
				o.mu.Lock()
				o.async = on != 0
				o.mu.Unlock()
			}
			return sOK
		})
		asyncVtbl[4] = syscall.NewCallback(func(this, pOn uintptr) uintptr { // GetAsyncMode
			if o := dataFromAsync(this); o != nil && pOn != 0 {
				o.mu.Lock()
				*(*int32)(unsafe.Pointer(pOn)) = boolInt(o.async)
				o.mu.Unlock()
			}
			return sOK
		})
		asyncVtbl[5] = syscall.NewCallback(func(this, ctx uintptr) uintptr { // StartOperation
			if o := dataFromAsync(this); o != nil {
				o.mu.Lock()
				o.inOp = true
				o.mu.Unlock()
			}
			return sOK
		})
		asyncVtbl[6] = syscall.NewCallback(func(this, pIn uintptr) uintptr { // InOperation
			if o := dataFromAsync(this); o != nil && pIn != 0 {
				o.mu.Lock()
				*(*int32)(unsafe.Pointer(pIn)) = boolInt(o.inOp)
				o.mu.Unlock()
			}
			return sOK
		})
		asyncVtbl[7] = syscall.NewCallback(func(this, hr, ctx, effects uintptr) uintptr { // EndOperation
			if o := dataFromAsync(this); o != nil {
				o.mu.Lock()
				o.inOp = false
				o.mu.Unlock()
			}
			return sOK
		})

		// --- IDropSource
		sourceVtbl[0] = syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			switch guidAt(riid) {
			case iidIUnknown, iidIDropSource:
				setOut(ppv, this)
				sourceAddRef(this)
				return sOK
			}
			setOut(ppv, 0)
			return eNoInterface
		})
		sourceVtbl[1] = syscall.NewCallback(func(this uintptr) uintptr { return sourceAddRef(this) })
		sourceVtbl[2] = syscall.NewCallback(func(this uintptr) uintptr {
			liveMu.Lock()
			defer liveMu.Unlock()
			s, _ := liveObjects[this].(*dropSource)
			if s == nil {
				return 0
			}
			s.refs--
			if s.refs <= 0 {
				delete(liveObjects, this)
				return 0
			}
			return uintptr(s.refs)
		})
		sourceVtbl[3] = syscall.NewCallback(func(this, escape, keys uintptr) uintptr { // QueryContinueDrag
			if escape != 0 {
				return dragdropSCancel
			}
			liveMu.Lock()
			s, _ := liveObjects[this].(*dropSource)
			liveMu.Unlock()
			mk := uintptr(0x1) // MK_LBUTTON
			if s != nil && s.btn == vkRButton {
				mk = 0x2
			}
			if keys&mk == 0 {
				return dragdropSDrop
			}
			return sOK
		})
		sourceVtbl[4] = syscall.NewCallback(func(this, effect uintptr) uintptr { return dragdropSUseDefCursors })
	})
}

func sourceAddRef(this uintptr) uintptr {
	liveMu.Lock()
	defer liveMu.Unlock()
	if s, ok := liveObjects[this].(*dropSource); ok {
		s.refs++
		return uintptr(s.refs)
	}
	return 0
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

func (o *dataObject) addRef() int32 {
	liveMu.Lock()
	defer liveMu.Unlock()
	o.refs++
	return o.refs
}

func (o *dataObject) release(this uintptr) int32 {
	liveMu.Lock()
	defer liveMu.Unlock()
	o.refs--
	if o.refs <= 0 {
		delete(liveObjects, this)
		return 0
	}
	return o.refs
}

func (o *dataObject) queryGetData(f *formatEtc) uintptr {
	switch {
	case f.cfFormat == cfFileGroupDescW && f.tymed&tymedHGlobal != 0:
	case f.cfFormat == cfPreferredEffect && f.tymed&tymedHGlobal != 0:
	case f.cfFormat == cfFileContents && f.tymed&tymedIStream != 0:
	default:
		return dvEFormatEtc
	}
	return sOK
}

func (o *dataObject) getData(f *formatEtc, m *stgMedium) uintptr {
	*m = stgMedium{}
	switch {
	case f.cfFormat == cfFileGroupDescW && f.tymed&tymedHGlobal != 0:
		size := 4 + fileDescriptorSize*len(o.files)
		h, ptr := globalAlloc(size)
		if h == 0 {
			return eOutOfMemory
		}
		buf := unsafe.Slice((*byte)(unsafe.Pointer(ptr)), size)
		*(*uint32)(unsafe.Pointer(&buf[0])) = uint32(len(o.files))
		for i, file := range o.files {
			d := buf[4+i*fileDescriptorSize:]
			*(*uint32)(unsafe.Pointer(&d[0])) = fdAttributes | fdFileSize | fdProgressUI
			*(*uint32)(unsafe.Pointer(&d[36])) = fileAttributeNormal
			*(*uint32)(unsafe.Pointer(&d[64])) = uint32(uint64(file.Size) >> 32)
			*(*uint32)(unsafe.Pointer(&d[68])) = uint32(file.Size)
			name, _ := windows.UTF16FromString(file.Name)
			if len(name) > 259 {
				name = append(name[:259], 0)
			}
			for j, c := range name {
				*(*uint16)(unsafe.Pointer(&d[72+2*j])) = c
			}
		}
		procGlobalUnlock.Call(h)
		m.tymed, m.handle = tymedHGlobal, h
		return sOK
	case f.cfFormat == cfPreferredEffect && f.tymed&tymedHGlobal != 0:
		h, ptr := globalAlloc(4)
		if h == 0 {
			return eOutOfMemory
		}
		*(*uint32)(unsafe.Pointer(ptr)) = dropEffectCopy
		procGlobalUnlock.Call(h)
		m.tymed, m.handle = tymedHGlobal, h
		return sOK
	case f.cfFormat == cfFileContents && f.tymed&tymedIStream != 0:
		i := int(f.lindex)
		if i < 0 || i >= len(o.files) {
			return dvEFormatEtc
		}
		p, _ := windows.UTF16PtrFromString(o.files[i].Path)
		var stream uintptr
		hr, _, _ := procSHCreateStreamOnFileEx.Call(uintptr(unsafe.Pointer(p)), stgmReadShareDenyWrite,
			fileAttributeNormal, 0, 0, uintptr(unsafe.Pointer(&stream)))
		if int32(hr) < 0 {
			return hr
		}
		m.tymed, m.handle = tymedIStream, stream
		return sOK
	}
	return dvEFormatEtc
}

func globalAlloc(size int) (uintptr, uintptr) {
	h, _, _ := procGlobalAlloc.Call(gmemMoveableZero, uintptr(size))
	if h == 0 {
		return 0, 0
	}
	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return 0, 0
	}
	return h, ptr
}

// ---------------------------------------------------------------- drag

// buttonDown reports which mouse button (if any) is held right now.
func buttonDown() uintptr {
	left, right := uintptr(vkLButton), uintptr(vkRButton)
	if swapped, _, _ := procGetSystemMetrics.Call(23); swapped != 0 { // SM_SWAPBUTTON
		left, right = right, left
	}
	if s, _, _ := procGetAsyncKeyState.Call(left); s&0x8000 != 0 {
		return vkLButton
	}
	if s, _, _ := procGetAsyncKeyState.Call(right); s&0x8000 != 0 {
		return vkRButton
	}
	return 0
}

// dragFiles runs a drag-and-drop of the given files. Must be called on the
// UI thread while the mouse button is still held.
func dragFiles(files []app.DragFile) error {
	btn := buttonDown()
	if btn == 0 {
		return nil // button already released: nothing to drag
	}
	registerFormats()
	buildVtables()
	procOleInitialize.Call(0)

	// async = true tells Explorer it may copy on a background thread.
	obj := &dataObject{vtbl: &dataVtbl, asyncVtbl: &asyncVtbl, refs: 1, files: files, async: true}
	src := &dropSource{vtbl: &sourceVtbl, refs: 1, btn: btn}
	objPtr := uintptr(unsafe.Pointer(obj))
	srcPtr := uintptr(unsafe.Pointer(src))
	liveMu.Lock()
	liveObjects[objPtr] = obj
	liveObjects[srcPtr] = src
	liveMu.Unlock()

	var effect uint32
	procDoDragDrop.Call(objPtr, srcPtr, dropEffectCopy, uintptr(unsafe.Pointer(&effect)))

	// Drop our own references. Explorer may still hold the data object
	// while it copies in the background; it stays registered until then.
	comCall(srcPtr, vtRelease)
	comCall(objPtr, vtRelease)
	return nil
}

func (p *winPlatform) DragOut(files []app.DragFile) error {
	var err error
	p.onUI(func() { err = dragFiles(files) })
	return err
}
