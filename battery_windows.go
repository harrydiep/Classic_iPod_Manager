//go:build windows

package main

import (
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

// hidInfo describes one HID interface (collection) from Apple.
type hidInfo struct {
	path             string
	pid              uint16
	serial           string
	usagePage, usage uint16
	inLen, outLen    int
	out              []iap.Report
	in               map[byte]int
	err              error // why its details couldn't be read
}

// listAppleHID lists the HID interfaces with Apple's vendor ID.
func listAppleHID() ([]hidInfo, error) {
	var guid windows.GUID
	procHidD_GetHidGuid.Call(uintptr(unsafe.Pointer(&guid)))
	paths, err := windows.CM_Get_Device_Interface_List("", &guid, windows.CM_GET_DEVICE_INTERFACE_LIST_PRESENT)
	if err != nil {
		return nil, err
	}
	var out []hidInfo
	for _, p := range paths {
		// Only VID_05AC devices are opened at all.
		if !strings.Contains(strings.ToUpper(p), "VID_05AC") {
			continue
		}
		out = append(out, describeHID(p))
	}
	return out, nil
}

// describeHID reads an interface's IDs, serial and report layout. Access 0
// is enough for all of these, so nothing is opened for reading or writing.
func describeHID(path string) hidInfo {
	info := hidInfo{path: path}
	pp, err := windows.UTF16PtrFromString(path)
	if err != nil {
		info.err = err
		return info
	}
	h, err := windows.CreateFile(pp, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		info.err = err
		return info
	}
	defer windows.CloseHandle(h)
	attr := hiddAttributes{Size: uint32(unsafe.Sizeof(hiddAttributes{}))}
	if r, _, e := procHidD_GetAttributes.Call(uintptr(h), uintptr(unsafe.Pointer(&attr))); r&0xff == 0 {
		info.err = fmt.Errorf("HidD_GetAttributes: %v", e)
		return info
	}
	info.pid = attr.ProductID
	var buf [128]uint16
	if r, _, _ := procHidD_GetSerialNumberString.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2)); r&0xff != 0 {
		info.serial = windows.UTF16ToString(buf[:])
	}
	var pre uintptr
	if r, _, e := procHidD_GetPreparsedData.Call(uintptr(h), uintptr(unsafe.Pointer(&pre))); r&0xff == 0 {
		info.err = fmt.Errorf("HidD_GetPreparsedData: %v", e)
		return info
	}
	defer procHidD_FreePreparsedData.Call(pre)
	var caps hidpCaps
	if st, _, _ := procHidP_GetCaps.Call(pre, uintptr(unsafe.Pointer(&caps))); uint32(st) != hidpStatusSuccess {
		info.err = fmt.Errorf("HidP_GetCaps failed (0x%X)", uint32(st))
		return info
	}
	info.usagePage, info.usage = caps.UsagePage, caps.Usage
	info.inLen, info.outLen = int(caps.InputReportByteLength), int(caps.OutputReportByteLength)
	info.out = reportSizes(pre, hidpOutput, int(caps.NumberOutputValueCaps))
	if len(info.out) == 0 && info.outLen > 1 {
		info.out = []iap.Report{{ID: 0, Size: info.outLen - 1}}
	}
	info.in = map[byte]int{}
	for _, r := range reportSizes(pre, hidpInput, int(caps.NumberInputValueCaps)) {
		info.in[r.ID] = r.Size
	}
	return info
}

// usable reports whether the interface can carry iAP packets.
func (h hidInfo) usable() bool { return h.err == nil && h.inLen >= 3 && h.outLen >= 3 }

// isIPodPID reports whether pid belongs to an iPod in disk mode (classic,
// mini and nano families, 0x1201-0x126F).
func isIPodPID(pid uint16) bool { return pid > 0x1200 && pid < 0x1270 }

func normSerial(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.TrimLeft(strings.TrimPrefix(s, "0X"), "0")
}

// pickIPodHID chooses the iPod interfaces whose USB serial matches the
// iPod's FireWire GUID; with no match it only guesses when a single iPod is
// connected.
func pickIPodHID(all []hidInfo, serial string) []hidInfo {
	var cands []hidInfo
	for _, h := range all {
		if isIPodPID(h.pid) && h.usable() {
			cands = append(cands, h)
		}
	}
	var match []hidInfo
	if serial != "" {
		for _, c := range cands {
			if normSerial(c.serial) == normSerial(serial) {
				match = append(match, c)
			}
		}
	}
	if len(match) > 0 {
		return match
	}
	serials := map[string]bool{}
	for _, c := range cands {
		serials[normSerial(c.serial)] = true
	}
	if len(serials) == 1 {
		return cands
	}
	return nil
}

// hidPort is an opened HID interface used as an iap.Transport.
type hidPort struct {
	h        windows.Handle
	info     hidInfo
	ev       windows.Handle
	ov       *windows.Overlapped // on the heap: Windows holds on to it while I/O is pending
	writeWay int                 // 0 untried, 1 exact WriteFile, 2 padded WriteFile, 3 HidD_SetOutputReport
	rbuf     []byte
	log      func(string)
}

func openHIDPort(info hidInfo, log func(string)) (*hidPort, error) {
	pp, err := windows.UTF16PtrFromString(info.path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(pp, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, fmt.Errorf("opening the HID interface for reading and writing: %w", err)
	}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &hidPort{h: h, info: info, ev: ev, ov: new(windows.Overlapped), rbuf: make([]byte, info.inLen), log: log}, nil
}

func (p *hidPort) Close() {
	windows.CloseHandle(p.ev)
	windows.CloseHandle(p.h)
}

func (p *hidPort) logf(format string, a ...interface{}) {
	if p.log != nil {
		p.log(fmt.Sprintf(format, a...))
	}
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
	*p.ov = windows.Overlapped{HEvent: p.ev}
	var n uint32
	var err error
	if write {
		err = windows.WriteFile(p.h, buf, &n, p.ov)
	} else {
		err = windows.ReadFile(p.h, buf, &n, p.ov)
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
		windows.CancelIoEx(p.h, p.ov)
		if err := windows.GetOverlappedResult(p.h, p.ov, &n, true); err == nil && n > 0 {
			return int(n), nil // finished just as it was cancelled
		}
		return 0, iap.ErrTimeout
	}
	if err := windows.GetOverlappedResult(p.h, p.ov, &n, false); err != nil {
		return 0, err
	}
	return int(n), nil
}

// Write sends one report. Windows drivers differ in whether they want the
// report's own size or the longest output report, so both are tried, then
// HidD_SetOutputReport.
func (p *hidPort) Write(rep []byte) error {
	padded := make([]byte, p.info.outLen)
	copy(padded, rep)
	names := [...]string{"", "WriteFile (report size)", "WriteFile (padded)", "HidD_SetOutputReport"}
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
			p.logf("   writing with %s", names[way])
			return nil
		}
		p.logf("   %s failed: %v", names[way], err)
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

// battery finds the iPod's accessory interface and asks for its battery,
// logging each step if log is set.
func battery(serial string, log func(string)) (iap.Battery, error) {
	logf := func(format string, a ...interface{}) {
		if log != nil {
			log(fmt.Sprintf(format, a...))
		}
	}
	all, err := listAppleHID()
	if err != nil {
		logf("listing HID interfaces failed: %v", err)
		return iap.Battery{}, err
	}
	cands := pickIPodHID(all, serial)
	if len(cands) == 0 {
		logf("no usable iPod HID interface")
		return iap.Battery{}, app.ErrNoAccessoryPort
	}
	var lastErr error = app.ErrNoAccessoryPort
	for _, c := range cands { // an iPod can have more than one HID collection
		logf("\nTalking to %s", c.path)
		port, err := openHIDPort(c, log)
		if err != nil {
			logf("   %v", err)
			lastErr = err
			continue
		}
		b, err := iap.QueryBattery(port, c.out, c.in, log)
		port.Close()
		if err == nil {
			return b, nil
		}
		logf("   result: %v", err)
		lastErr = err
	}
	return iap.Battery{}, lastErr
}

// Battery asks the iPod for its battery state.
func (p *winPlatform) Battery(serial string) (iap.Battery, error) {
	return battery(serial, nil)
}

// BatteryReport runs the battery request with every step written down,
// along with what Windows reports about Apple's USB devices.
func (p *winPlatform) BatteryReport(serial string) (iap.Battery, string, error) {
	var sb strings.Builder
	w := func(line string) { sb.WriteString(line + "\n") }
	wf := func(format string, a ...interface{}) { w(fmt.Sprintf(format, a...)) }
	wf("Classic iPod Manager %s battery report, %s", app.Version, time.Now().Format("2006-01-02 15:04:05"))
	if serial == "" {
		w("iPod FireWire GUID: not found in SysInfo")
	} else {
		wf("iPod FireWire GUID: %s", serial)
	}

	w("\nApple USB devices:")
	usb := listAppleUSB()
	if len(usb) == 0 {
		w("  (none)")
	}
	for _, l := range usb {
		w("  " + l)
	}

	w("\nApple HID interfaces:")
	all, err := listAppleHID()
	if err != nil {
		wf("  listing failed: %v", err)
	}
	if len(all) == 0 && err == nil {
		w("  (none)")
	}
	for _, h := range all {
		wf("  %s", h.path)
		if h.err != nil {
			wf("    couldn't read details: %v", h.err)
			continue
		}
		wf("    PID %04X, serial %q, usage page %04X usage %04X", h.pid, h.serial, h.usagePage, h.usage)
		wf("    input %d bytes %v, output %d bytes %v", h.inLen, h.in, h.outLen, h.out)
	}

	w("\nBattery request:")
	b, err := battery(serial, w)
	if err != nil {
		wf("\nResult: failed: %v", err)
	} else {
		wf("\nResult: %s, level %d/255 (%d%%)", b.Describe(), b.Level, b.Percent())
	}
	return b, sb.String(), err
}

// listAppleUSB describes the present devices under the USB enumerator with
// Apple's vendor ID: instance ID, description and driver.
func listAppleUSB() []string {
	set, err := windows.SetupDiGetClassDevsEx(nil, "USB", 0, windows.DIGCF_ALLCLASSES|windows.DIGCF_PRESENT, 0, "")
	if err != nil {
		return []string{"listing failed: " + err.Error()}
	}
	defer set.Close()
	var out []string
	for i := 0; ; i++ {
		d, err := set.EnumDeviceInfo(i)
		if err != nil {
			break
		}
		id, err := set.DeviceInstanceID(d)
		if err != nil || !strings.Contains(strings.ToUpper(id), "VID_05AC") {
			continue
		}
		prop := func(p windows.SPDRP) string {
			v, err := set.DeviceRegistryProperty(d, p)
			if err != nil {
				return ""
			}
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprint(v)
		}
		out = append(out, fmt.Sprintf("%s | %s | driver: %s", id, prop(windows.SPDRP_DEVICEDESC), prop(windows.SPDRP_SERVICE)))
	}
	sort.Strings(out)
	return out
}
