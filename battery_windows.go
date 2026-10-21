//go:build windows

package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"podmanager/internal/app"
	"podmanager/internal/iap"
)

// The iPod's battery is read over the USB HID interface iPods expose next
// to the disk (the one car stereos use). See internal/iap for the protocol.

var (
	hidDLL                         = windows.NewLazySystemDLL("hid.dll")
	procHidD_GetHidGuid            = hidDLL.NewProc("HidD_GetHidGuid")
	procHidD_GetAttributes         = hidDLL.NewProc("HidD_GetAttributes")
	procHidD_GetSerialNumberString = hidDLL.NewProc("HidD_GetSerialNumberString")
	procHidD_GetPreparsedData      = hidDLL.NewProc("HidD_GetPreparsedData")
	procHidD_FreePreparsedData     = hidDLL.NewProc("HidD_FreePreparsedData")
	procHidD_SetOutputReport       = hidDLL.NewProc("HidD_SetOutputReport")
	procHidP_GetCaps               = hidDLL.NewProc("HidP_GetCaps")
	procHidP_GetValueCaps          = hidDLL.NewProc("HidP_GetValueCaps")
)

const (
	appleVID          = 0x05AC
	hidpStatusSuccess = 0x00110000
	hidpInput         = 0
	hidpOutput        = 1
)

type hiddAttributes struct {
	Size          uint32
	VendorID      uint16
	ProductID     uint16
	VersionNumber uint16
}

type hidpCaps struct {
	Usage, UsagePage                                uint16
	InputReportByteLength, OutputReportByteLength   uint16
	FeatureReportByteLength                         uint16
	Reserved                                        [17]uint16
	NumberLinkCollectionNodes                       uint16
	NumberInputButtonCaps, NumberInputValueCaps     uint16
	NumberInputDataIndices                          uint16
	NumberOutputButtonCaps, NumberOutputValueCaps   uint16
	NumberOutputDataIndices                         uint16
	NumberFeatureButtonCaps, NumberFeatureValueCaps uint16
	NumberFeatureDataIndices                        uint16
}

type hidpValueCaps struct {
	UsagePage                uint16
	ReportID                 uint8
	IsAlias                  uint8
	BitField                 uint16
	LinkCollection           uint16
	LinkUsage                uint16
	LinkUsagePage            uint16
	IsRange, IsStringRange   uint8
	IsDesignatorRange        uint8
	IsAbsolute, HasNull      uint8
	Reserved                 uint8
	BitSize                  uint16
	ReportCount              uint16
	Reserved2                [5]uint16
	UnitsExp, Units          uint32
	LogicalMin, LogicalMax   int32
	PhysicalMin, PhysicalMax int32
	Usage                    [8]uint16
}

// The structures must match the Windows SDK layout exactly.
var (
	_ [64]byte = [unsafe.Sizeof(hidpCaps{})]byte{}
	_ [72]byte = [unsafe.Sizeof(hidpValueCaps{})]byte{}
)

// isIPodPID reports whether pid belongs to an iPod in disk mode (classic,
// mini and nano families, 0x1201-0x126F).
func isIPodPID(pid uint16) bool { return pid > 0x1200 && pid < 0x1270 }

type hidCandidate struct {
	path   string
	serial string
	pid    uint16
}

// findIPodHID lists the HID interfaces of connected iPods.
func findIPodHID() ([]hidCandidate, error) {
	var guid windows.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	paths, err := windows.CM_Get_Device_Interface_List("", &guid, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, err
	}
	var out []hidCandidate
	for _, p := range paths {
		// Only VID_05AC devices are opened at all.
		if !strings.Contains(strings.ToUpper(p), "VID_05AC") {
			continue
		}
		pp, err := windows.UTF16PtrFromString(p)
		if err != nil {
			continue
		}
		// Access 0 is enough to read attributes and strings.
		h, err := windows.CreateFile(pp, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			continue
		}
		attr := hiddAttributes{Size: uint32(unsafe.Sizeof(hiddAttributes{}))}
		r, _, _ := procHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr)))
		if r&0xff != 0 && attr.VendorID == appleVID && isIPodPID(attr.ProductID) {
			var buf [128]uint16
			serial := ""
			if r, _, _ := procHidD_GetSerialNumberString.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2)); r&0xff != 0 {
				serial = windows.UTF16ToString(buf[:])
			}
			out = append(out, hidCandidate{path: p, serial: serial, pid: attr.ProductID})
		}
		windows.CloseHandle(h)
	}
	return out, nil
}

// pickIPodHID chooses the interface whose USB serial matches the iPod's
// FireWire GUID; with no match it only guesses when a single iPod is
// connected.
func pickIPodHID(cands []hidCandidate, serial string) []hidCandidate {
	norm := func(s string) string {
		s = strings.ToUpper(strings.TrimSpace(s))
		return strings.TrimLeft(strings.TrimPrefix(s, "0X"), "0")
	}
	var match []hidCandidate
	if serial != "" {
		for _, c := range cands {
			if norm(c.serial) == norm(serial) {
				match = append(match, c)
			}
		}
	}
	if len(match) > 0 {
		return match
	}
	serials := map[string]bool{}
	for _, c := range cands {
		serials[norm(c.serial)] = true
	}
	if len(serials) == 1 {
		return cands
	}
	return nil
}

// hidPort is an opened HID interface used as an iap.Transport.
type hidPort struct {
	h        windows.Handle
	inLen    int
	outLen   int
	out      []iap.Report
	in       map[byte]int
	ev       windows.Handle
	writeWay int // 0 untried, 1 exact WriteFile, 2 padded WriteFile, 3 HidD_SetOutputReport
	rbuf     []byte
}

func openHIDPort(path string) (*hidPort, error) {
	pp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(pp, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	p := &hidPort{h: h}
	if err := p.readCaps(); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	p.ev, err = windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	p.rbuf = make([]byte, p.inLen)
	return p, nil
}

func (p *hidPort) Close() {
	windows.CloseHandle(p.ev)
	windows.CloseHandle(p.h)
}

func (p *hidPort) readCaps() error {
	var pre uintptr
	if r, _, _ := procHidD_GetPreparsedData.Call(uintptr(p.h), uintptr(unsafe.Pointer(&pre))); r&0xff == 0 {
		return errors.New("couldn't read the HID descriptor")
	}
	defer procHidD_FreePreparsedData.Call(pre)
	var caps hidpCaps
	if st, _, _ := procHidP_GetCaps.Call(pre, uintptr(unsafe.Pointer(&caps))); uint32(st) != hidpStatusSuccess {
		return fmt.Errorf("HidP_GetCaps failed (0x%X)", uint32(st))
	}
	p.inLen, p.outLen = int(caps.InputReportByteLength), int(caps.OutputReportByteLength)
	if p.inLen < 3 || p.outLen < 3 {
		return errors.New("this HID interface doesn't carry accessory data")
	}
	p.out = reportSizes(pre, hidpOutput, int(caps.NumberOutputValueCaps))
	if len(p.out) == 0 {
		p.out = []iap.Report{{ID: 0, Size: p.outLen - 1}}
	}
	p.in = map[byte]int{}
	for _, r := range reportSizes(pre, hidpInput, int(caps.NumberInputValueCaps)) {
		p.in[r.ID] = r.Size
	}
	return nil
}

// reportSizes adds up the value fields of each report ID.
func reportSizes(pre uintptr, kind uint32, n int) []iap.Report {
	if n == 0 {
		return nil
	}
	caps := make([]hidpValueCaps, n)
	l := uint16(n)
	if st, _, _ := procHidP_GetValueCaps.Call(uintptr(kind), uintptr(unsafe.Pointer(&caps[0])), uintptr(unsafe.Pointer(&l)), pre); uint32(st) != hidpStatusSuccess {
		return nil
	}
	bits := map[byte]int{}
	for _, c := range caps[:l] {
		bits[c.ReportID] += int(c.BitSize) * int(c.ReportCount)
	}
	var out []iap.Report
	for id, b := range bits {
		if b >= 16 {
			out = append(out, iap.Report{ID: id, Size: b / 8})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	return out
}

// io runs one overlapped ReadFile/WriteFile with a timeout.
func (p *hidPort) io(write bool, buf []byte, timeout time.Duration) (int, error) {
	windows.ResetEvent(p.ev)
	ov := windows.Overlapped{HEvent: p.ev}
	var n uint32
	var err error
	if write {
		err = windows.WriteFile(p.h, buf, &n, &ov)
	} else {
		err = windows.ReadFile(p.h, buf, &n, &ov)
	}
	if err == nil {
		return int(n), nil
	}
	if err != windows.ERROR_IO_PENDING {
		return 0, err
	}
	ms := uint32(timeout / time.Millisecond)
	if ms == 0 {
		ms = 1
	}
	ev, _ := windows.WaitForSingleObject(p.ev, ms)
	if ev != windows.WAIT_OBJECT_0 {
		windows.CancelIoEx(p.h, &ov)
		if err := windows.GetOverlappedResult(p.h, &ov, &n, true); err == nil && n > 0 {
			return int(n), nil // finished just as it was cancelled
		}
		return 0, iap.ErrTimeout
	}
	if err := windows.GetOverlappedResult(p.h, &ov, &n, false); err != nil {
		return 0, err
	}
	return int(n), nil
}

// Write sends one report. Windows drivers differ in whether they want the
// report's own size or the longest output report, so both are tried, then
// HidD_SetOutputReport.
func (p *hidPort) Write(rep []byte) error {
	padded := make([]byte, p.outLen)
	copy(padded, rep)
	try := func(way int) error {
		switch way {
		case 1:
			_, err := p.io(true, rep, time.Second)
			return err
		case 2:
			_, err := p.io(true, padded, time.Second)
			return err
		default:
			r, _, e := procHidD_SetOutputReport.Call(uintptr(p.h), uintptr(unsafe.Pointer(&padded[0])), uintptr(len(padded)))
			if r&0xff == 0 {
				return e
			}
			return nil
		}
	}
	if p.writeWay != 0 {
		return try(p.writeWay)
	}
	var err error
	for way := 1; way <= 3; way++ {
		if len(rep) == len(padded) && way == 2 {
			continue
		}
		if err = try(way); err == nil {
			p.writeWay = way
			return nil
		}
	}
	return err
}

// Read returns the next input report.
func (p *hidPort) Read(timeout time.Duration) ([]byte, error) {
	n, err := p.io(false, p.rbuf, timeout)
	if err != nil {
		return nil, err
	}
	return append([]byte(nil), p.rbuf[:n]...), nil
}

// Battery finds the iPod's accessory interface and asks for its battery.
func (p *winPlatform) Battery(serial string) (iap.Battery, error) {
	cands, err := findIPodHID()
	if err != nil {
		return iap.Battery{}, err
	}
	cands = pickIPodHID(cands, serial)
	if len(cands) == 0 {
		return iap.Battery{}, app.ErrNoAccessoryPort
	}
	var lastErr error = app.ErrNoAccessoryPort
	for _, c := range cands { // an iPod can have more than one HID collection
		port, err := openHIDPort(c.path)
		if err != nil {
			lastErr = err
			continue
		}
		b, err := iap.QueryBattery(port, port.out, port.in)
		port.Close()
		if err == nil {
			return b, nil
		}
		lastErr = err
	}
	return iap.Battery{}, lastErr
}
