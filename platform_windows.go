//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"podmanager/internal/app"
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	procSetErrorMode     = kernel32.NewProc("SetErrorMode")
	user32               = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
)

type winPlatform struct {
	hwnd     uintptr
	dispatch func(func())
}

func init() {
	// Don't show "There is no disk in the drive" dialogs while scanning drives.
	procSetErrorMode.Call(0x0001 | 0x8000) // SEM_FAILCRITICALERRORS | SEM_NOOPENFILEERRORBOX
}

func messageBox(title, text string, flags uint32) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	procMessageBoxW.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), uintptr(flags))
}

func (p *winPlatform) Roots() []string {
	mask, _, _ := procGetLogicalDrives.Call()
	var roots []string
	for i := 2; i < 26; i++ { // skip A: and B:
		if mask&(1<<uint(i)) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+i)
		rp, _ := windows.UTF16PtrFromString(root)
		switch windows.GetDriveType(rp) {
		case windows.DRIVE_REMOVABLE, windows.DRIVE_FIXED:
			roots = append(roots, root)
		}
	}
	return roots
}

func (p *winPlatform) VolumeInfo(root string) (string, uint64, uint64, error) {
	rp, _ := windows.UTF16PtrFromString(root)
	var name [261]uint16
	var serial, maxLen, flags uint32
	var fs [261]uint16
	label := ""
	if err := windows.GetVolumeInformation(rp, &name[0], uint32(len(name)), &serial, &maxLen, &flags, &fs[0], uint32(len(fs))); err == nil {
		label = windows.UTF16ToString(name[:])
	}
	var free, total, totalFree uint64
	err := windows.GetDiskFreeSpaceEx(rp, &free, &total, &totalFree)
	return label, total, free, err
}

const (
	fsctlLockVolume          = 0x00090018
	fsctlDismountVolume      = 0x00090020
	ioctlStorageMediaRemoval = 0x002D4804
	ioctlStorageEjectMedia   = 0x002D4808
)

func (p *winPlatform) Eject(root string) error {
	vol := `\\.\` + strings.TrimRight(filepath.VolumeName(root), `\`)
	vp, _ := windows.UTF16PtrFromString(vol)
	h, err := windows.CreateFile(vp, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return p.shellEject(root, err)
	}
	closed := false
	closeH := func() {
		if !closed {
			windows.CloseHandle(h)
			closed = true
		}
	}
	defer closeH()
	windows.FlushFileBuffers(h)
	var ret uint32
	locked := false
	for i := 0; i < 20; i++ {
		if windows.DeviceIoControl(h, fsctlLockVolume, nil, 0, nil, 0, &ret, nil) == nil {
			locked = true
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !locked {
		return errors.New("Windows is still using the iPod. Close any Explorer windows or programs using it, then try again.")
	}
	if err := windows.DeviceIoControl(h, fsctlDismountVolume, nil, 0, nil, 0, &ret, nil); err != nil {
		return fmt.Errorf("couldn't dismount the iPod: %v", err)
	}
	prevent := byte(0)
	windows.DeviceIoControl(h, ioctlStorageMediaRemoval, &prevent, 1, nil, 0, &ret, nil)
	if err := windows.DeviceIoControl(h, ioctlStorageEjectMedia, nil, 0, nil, 0, &ret, nil); err != nil {
		closeH()
		return p.shellEject(root, err)
	}
	return nil
}

// shellEject falls back to Explorer's own "Eject" command.
func (p *winPlatform) shellEject(root string, cause error) error {
	drive := strings.TrimRight(filepath.VolumeName(root), `\`)
	script := fmt.Sprintf(`(New-Object -ComObject Shell.Application).Namespace(17).ParseName('%s').InvokeVerb('Eject')`, drive)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("couldn't eject the iPod (%v)", cause)
	}
	return nil
}

func (p *winPlatform) Reveal(path string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	return cmd.Start()
}

// onUI runs fn on the window's thread and waits for it.
func (p *winPlatform) onUI(fn func()) {
	done := make(chan struct{})
	p.dispatch(func() {
		defer close(done)
		fn()
	})
	<-done
}

func (p *winPlatform) PickFiles() ([]string, error) {
	var res []string
	var err error
	p.onUI(func() {
		res, err = fileDialog(p.hwnd, "Add Songs to iPod", false, true, []filterSpec{
			{"Songs (MP3, AAC, Apple Lossless, WAV, AIFF)", "*.mp3;*.m4a;*.m4b;*.mp4;*.aac;*.wav;*.aif;*.aiff"},
			{"All files", "*.*"},
		})
	})
	return res, err
}

// PickAny shows an Open dialog with the given file types.
func (p *winPlatform) PickAny(title string, filters []app.FileFilter, multi bool) ([]string, error) {
	var specs []filterSpec
	for _, f := range filters {
		specs = append(specs, filterSpec{f.Name, f.Pattern})
	}
	var res []string
	var err error
	p.onUI(func() {
		res, err = fileDialog(p.hwnd, title, false, multi, specs)
	})
	return res, err
}

// OpenFolder shows a folder in Explorer.
func (p *winPlatform) OpenFolder(path string) error {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + path + `"`}
	return cmd.Start()
}

func (p *winPlatform) PickFolder(title string) (string, error) {
	var res []string
	var err error
	p.onUI(func() {
		res, err = fileDialog(p.hwnd, title, true, false, nil)
	})
	if err != nil || len(res) == 0 {
		return "", err
	}
	return res[0], nil
}

var procGetDpiForSystem = user32.NewProc("GetDpiForSystem")

// windowSize picks a starting window size (in physical pixels) that allows
// for Windows display scaling and still fits on the screen.
func windowSize() (w, h, minW, minH uint) {
	dpi := uintptr(96)
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d >= 96 {
			dpi = d
		}
	}
	sw, _, _ := procGetSystemMetrics.Call(0) // SM_CXSCREEN
	sh, _, _ := procGetSystemMetrics.Call(1) // SM_CYSCREEN
	scale := func(v uint) uint { return v * uint(dpi) / 96 }
	fit := func(v, screen uint, frac float64) uint {
		if limit := uint(float64(screen) * frac); screen > 0 && v > limit {
			return limit
		}
		return v
	}
	w = fit(scale(1380), uint(sw), 0.94)
	h = fit(scale(800), uint(sh), 0.88)
	minW = fit(scale(900), uint(sw), 0.9)
	minH = fit(scale(580), uint(sh), 0.85)
	return
}
