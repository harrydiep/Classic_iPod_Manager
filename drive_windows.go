//go:build windows

package main

import (
	"encoding/hex"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// driveSerial returns the USB serial number of the drive at root ("E:\"),
// which for an iPod is its FireWire ID: the iPod classic and 3rd/4th
// generation nano need it to sign their database. Opening the volume
// without read or write access is enough for this query, so it works
// without administrator rights.
func driveSerial(root string) string {
	vol := `\\.\` + strings.TrimRight(root, `\/`)
	p, err := windows.UTF16PtrFromString(vol)
	if err != nil {
		return ""
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)
	const ioctlStorageQueryProperty = 0x2D1400
	query := struct {
		propertyID uint32 // StorageDeviceProperty
		queryType  uint32 // PropertyStandardQuery
		extra      [4]byte
	}{}
	buf := make([]byte, 1024)
	var n uint32
	if err := windows.DeviceIoControl(h, ioctlStorageQueryProperty, (*byte)(unsafe.Pointer(&query)), uint32(unsafe.Sizeof(query)),
		&buf[0], uint32(len(buf)), &n, nil); err != nil || n < 28 {
		return ""
	}
	// STORAGE_DEVICE_DESCRIPTOR: SerialNumberOffset at +24
	off := int(uint32(buf[24]) | uint32(buf[25])<<8 | uint32(buf[26])<<16 | uint32(buf[27])<<24)
	if off <= 0 || off >= int(n) {
		return ""
	}
	end := off
	for end < int(n) && buf[end] != 0 {
		end++
	}
	s := strings.TrimSpace(string(buf[off:end]))
	// some Windows versions return the serial hex-encoded
	if len(s) == 32 {
		if b, err := hex.DecodeString(s); err == nil {
			if d := strings.TrimSpace(string(b)); len(d) == 16 {
				if _, err := hex.DecodeString(d); err == nil {
					s = d
				}
			}
		}
	}
	return s
}
