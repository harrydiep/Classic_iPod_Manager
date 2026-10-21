// Package iap speaks just enough of the iPod Accessory Protocol (iAP) to
// ask an iPod for its battery level and charging state.
//
// iPods from the 5th generation on expose a USB HID interface next to the
// disk. Car stereos and docks use it to carry iAP packets; PodManager uses
// it the same way, identifying itself as a "display remote" (lingo 3) and
// sending GetPowerBatteryState. Older firmware that doesn't know that
// command is asked with GetiPodStateInfo instead.
//
// Over USB each iAP packet (0x55, length, lingo, command, data, checksum;
// the 0xFF sync byte used on serial links is dropped) is carried in HID
// reports of the form [report ID][link control][packet bytes...]. A packet
// longer than one report continues in the next one.
package iap

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// Lingoes and commands used here.
const (
	LingoGeneral       = 0x00
	LingoDisplayRemote = 0x03

	cmdGeneralAck            = 0x02
	cmdIdentify              = 0x01 // deprecated single-lingo identify, no reply
	cmdIdentifyDeviceLingoes = 0x13

	cmdRemoteAck            = 0x00
	cmdGetiPodStateInfo     = 0x0C
	cmdRetiPodStateInfo     = 0x0D
	cmdGetPowerBatteryState = 0x1A
	cmdRetPowerBatteryState = 0x1B
	stateInfoPowerBattery   = 0x05
	linkContinuation        = 0x01
	linkMoreToFollow        = 0x02
	startByte               = 0x55
	maxSmallPayload         = 0xFC
	defaultTimeout          = 1500 * time.Millisecond
)

// Report is one HID report the device accepts or sends: its ID and how
// many bytes follow the ID.
type Report struct {
	ID   byte
	Size int
}

// Transport moves raw HID reports. Write gets [ID][payload...] sized for
// that report; Read returns the next input report including its ID byte.
type Transport interface {
	Write(report []byte) error
	Read(timeout time.Duration) ([]byte, error)
}

// ErrTimeout is returned by a Transport when nothing arrives in time.
var ErrTimeout = errors.New("the iPod didn't answer")

// Packet is a decoded iAP packet.
type Packet struct {
	Lingo, Cmd byte
	Data       []byte
}

// Encode builds an iAP packet without the serial sync byte.
func Encode(lingo, cmd byte, data []byte) []byte {
	n := 2 + len(data)
	var out []byte
	if n <= maxSmallPayload {
		out = append(out, startByte, byte(n))
	} else {
		out = append(out, startByte, 0, byte(n>>8), byte(n))
	}
	out = append(out, lingo, cmd)
	out = append(out, data...)
	var sum byte
	for _, b := range out[1:] {
		sum += b
	}
	return append(out, -sum)
}

// Decode parses one packet from the start of b (leading padding before the
// 0x55 start byte is skipped). It returns the packet and how many bytes it
// used, or ok=false if b doesn't hold a whole packet yet.
func Decode(b []byte) (p Packet, used int, ok bool, err error) {
	i := 0
	for i < len(b) && b[i] != startByte {
		i++
	}
	if i >= len(b) {
		return p, len(b), false, nil
	}
	h := i + 1
	if h >= len(b) {
		return p, i, false, nil
	}
	n, hdr := int(b[h]), 1
	if n == 0 {
		if h+2 >= len(b) {
			return p, i, false, nil
		}
		n, hdr = int(b[h+1])<<8|int(b[h+2]), 3
	}
	end := h + hdr + n // index of checksum
	if end >= len(b) {
		return p, i, false, nil
	}
	var sum byte
	for _, c := range b[h : end+1] {
		sum += c
	}
	if sum != 0 {
		return p, end + 1, false, errors.New("bad iAP checksum")
	}
	if n < 2 {
		return p, end + 1, false, errors.New("short iAP packet")
	}
	body := b[h+hdr : end]
	return Packet{Lingo: body[0], Cmd: body[1], Data: append([]byte(nil), body[2:]...)}, end + 1, true, nil
}

// Frame splits a packet into HID output reports, picking for each piece the
// smallest report that fits (or the largest available).
func Frame(packet []byte, out []Report) ([][]byte, error) {
	rs := make([]Report, 0, len(out))
	for _, r := range out {
		if r.Size >= 2 {
			rs = append(rs, r)
		}
	}
	if len(rs) == 0 {
		return nil, errors.New("the iPod has no usable output reports")
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].Size < rs[j].Size })
	var reports [][]byte
	for off := 0; off < len(packet) || off == 0; {
		left := len(packet) - off
		r := rs[len(rs)-1]
		for _, c := range rs {
			if c.Size-1 >= left {
				r = c
				break
			}
		}
		chunk := r.Size - 1
		if chunk > left {
			chunk = left
		}
		var link byte
		if off > 0 {
			link |= linkContinuation
		}
		if off+chunk < len(packet) {
			link |= linkMoreToFollow
		}
		rep := make([]byte, 1+r.Size)
		rep[0], rep[1] = r.ID, link
		copy(rep[2:], packet[off:off+chunk])
		reports = append(reports, rep)
		off += chunk
		if left == 0 {
			break
		}
	}
	return reports, nil
}

// Conn exchanges packets with an iPod over a Transport.
type Conn struct {
	T       Transport
	Out     []Report     // output reports, from the HID descriptor
	In      map[byte]int // input report sizes by ID (optional)
	Timeout time.Duration
	// Sync prefixes packets with the 0xFF sync byte used on serial links.
	// USB normally goes without it; some firmware may want it anyway.
	Sync bool
	// Log, if set, receives a line for every packet and report.
	Log     func(string)
	buf     []byte
	pending []Packet
}

func (c *Conn) logf(format string, a ...interface{}) {
	if c.Log != nil {
		c.Log(fmt.Sprintf(format, a...))
	}
}

// Send writes one packet.
func (c *Conn) Send(lingo, cmd byte, data []byte) error {
	pkt := Encode(lingo, cmd, data)
	if c.Sync {
		pkt = append([]byte{0xFF}, pkt...)
	}
	reps, err := Frame(pkt, c.Out)
	if err != nil {
		return err
	}
	c.logf("-> lingo %d cmd 0x%02X: % X", lingo, cmd, pkt)
	for _, r := range reps {
		if err := c.T.Write(r); err != nil {
			c.logf("   write of report % X failed: %v", r, err)
			return err
		}
	}
	return nil
}

// Next returns the next packet from the iPod.
func (c *Conn) Next(deadline time.Time) (Packet, error) {
	for {
		if len(c.pending) > 0 {
			p := c.pending[0]
			c.pending = c.pending[1:]
			return p, nil
		}
		left := time.Until(deadline)
		if left <= 0 {
			return Packet{}, ErrTimeout
		}
		rep, err := c.T.Read(left)
		if err != nil {
			return Packet{}, err
		}
		c.logf("   report in: % X", trimZeros(rep))
		if len(rep) < 2 {
			continue
		}
		payload := rep[2:]
		if n, ok := c.In[rep[0]]; ok && n-1 < len(payload) && n >= 1 {
			payload = payload[:n-1]
		}
		link := rep[1]
		if link&linkContinuation == 0 {
			c.buf = c.buf[:0] // a new packet starts; drop any half-received one
		}
		c.buf = append(c.buf, payload...)
		if link&linkMoreToFollow != 0 {
			continue
		}
		b := c.buf
		for len(b) > 0 {
			p, used, ok, err := Decode(b)
			if err != nil {
				c.logf("   undecodable data: %v", err)
				break
			}
			if !ok {
				break
			}
			c.logf("<- lingo %d cmd 0x%02X data % X", p.Lingo, p.Cmd, p.Data)
			c.pending = append(c.pending, p)
			b = b[used:]
		}
		c.buf = c.buf[:0]
	}
}

// await waits for a packet that match accepts, skipping notifications and
// other traffic.
func (c *Conn) await(match func(Packet) bool) (Packet, error) {
	t := c.Timeout
	if t == 0 {
		t = defaultTimeout
	}
	deadline := time.Now().Add(t)
	for {
		p, err := c.Next(deadline)
		if err != nil {
			return p, err
		}
		if match(p) {
			return p, nil
		}
	}
}

// AckError is a non-zero status in an iPod acknowledgement.
type AckError struct {
	Lingo, Cmd, Status byte
}

func (e *AckError) Error() string {
	return fmt.Sprintf("iPod refused command 0x%02X (lingo %d): status 0x%02X", e.Cmd, e.Lingo, e.Status)
}

// trimZeros drops trailing padding from a report for logging.
func trimZeros(b []byte) []byte {
	n := len(b)
	for n > 2 && b[n-1] == 0 {
		n--
	}
	return b[:n]
}

// IdentifyLingoes sends IdentifyDeviceLingoes (general + display remote).
// It returns the iPod's acknowledgement status, or ErrTimeout if the iPod
// didn't answer.
func (c *Conn) IdentifyLingoes() (byte, error) {
	lingoes := uint32(1<<LingoGeneral | 1<<LingoDisplayRemote)
	data := []byte{byte(lingoes >> 24), byte(lingoes >> 16), byte(lingoes >> 8), byte(lingoes), 0, 0, 0, 0, 0, 0, 0, 0}
	if err := c.Send(LingoGeneral, cmdIdentifyDeviceLingoes, data); err != nil {
		return 0, err
	}
	p, err := c.await(func(p Packet) bool {
		return p.Lingo == LingoGeneral && p.Cmd == cmdGeneralAck && len(p.Data) >= 2 && p.Data[1] == cmdIdentifyDeviceLingoes
	})
	if err != nil {
		return 0, err
	}
	return p.Data[0], nil
}

// IdentifyLegacy sends the older single-lingo Identify, which firmware
// without IdentifyDeviceLingoes understands. No acknowledgement is sent.
func (c *Conn) IdentifyLegacy() error {
	return c.Send(LingoGeneral, cmdIdentify, []byte{LingoDisplayRemote})
}

// Battery is the iPod's power state.
type Battery struct {
	State byte // see the State* constants
	Level byte // 0-255
}

// Power states reported by the iPod.
const (
	StateBatteryLow   = 0x00
	StateBattery      = 0x01
	StateExternalPack = 0x02 // powered by a battery pack, not charging
	StateExternal     = 0x03 // external power, not charging
	StateCharging     = 0x04
	StateCharged      = 0x05
)

// OnExternalPower reports whether the iPod is plugged into power.
func (b Battery) OnExternalPower() bool { return b.State >= StateExternalPack }

// Charging reports whether the battery is charging.
func (b Battery) Charging() bool { return b.State == StateCharging }

// Charged reports whether the battery is full and on external power.
func (b Battery) Charged() bool { return b.State == StateCharged }

// Percent returns the battery level as 0-100, or -1 if the iPod didn't
// report one (some firmware sends 0 while on external power).
func (b Battery) Percent() int {
	if b.State == StateCharged && b.Level == 0 {
		return 100
	}
	if b.Level == 0 && b.OnExternalPower() {
		return -1
	}
	return (int(b.Level)*100 + 127) / 255
}

// Describe is a short human description of the power state.
func (b Battery) Describe() string {
	switch b.State {
	case StateBatteryLow:
		return "Battery low"
	case StateBattery:
		return "On battery"
	case StateExternalPack:
		return "On battery pack, not charging"
	case StateExternal:
		return "Plugged in, not charging"
	case StateCharging:
		return "Charging"
	case StateCharged:
		return "Fully charged"
	}
	return fmt.Sprintf("Unknown power state 0x%02X", b.State)
}

// PowerBattery asks for the battery state. Call Identify first.
func (c *Conn) PowerBattery() (Battery, error) {
	if err := c.Send(LingoDisplayRemote, cmdGetPowerBatteryState, nil); err != nil {
		return Battery{}, err
	}
	p, err := c.await(func(p Packet) bool {
		if p.Lingo != LingoDisplayRemote {
			return false
		}
		return (p.Cmd == cmdRetPowerBatteryState && len(p.Data) >= 2) ||
			(p.Cmd == cmdRemoteAck && len(p.Data) >= 2 && p.Data[1] == cmdGetPowerBatteryState)
	})
	if err == nil && p.Cmd == cmdRetPowerBatteryState {
		return Battery{State: p.Data[0], Level: p.Data[1]}, nil
	}
	if err != nil && !errors.Is(err, ErrTimeout) {
		return Battery{}, err
	}
	// Older display remote lingo: GetiPodStateInfo(power/battery).
	if err := c.Send(LingoDisplayRemote, cmdGetiPodStateInfo, []byte{stateInfoPowerBattery}); err != nil {
		return Battery{}, err
	}
	p, err = c.await(func(p Packet) bool {
		if p.Lingo != LingoDisplayRemote {
			return false
		}
		return (p.Cmd == cmdRetiPodStateInfo && len(p.Data) >= 3 && p.Data[0] == stateInfoPowerBattery) ||
			(p.Cmd == cmdRemoteAck && len(p.Data) >= 2 && p.Data[1] == cmdGetiPodStateInfo)
	})
	if err != nil {
		return Battery{}, err
	}
	if p.Cmd == cmdRemoteAck {
		return Battery{}, &AckError{Lingo: LingoDisplayRemote, Cmd: cmdGetiPodStateInfo, Status: p.Data[0]}
	}
	return Battery{State: p.Data[1], Level: p.Data[2]}, nil
}

// QueryBattery identifies and asks for the battery state in one go. It
// tries USB framing first, then framing with the serial sync byte, and
// finally the older Identify command for firmware that ignores
// IdentifyDeviceLingoes. log may be nil.
func QueryBattery(t Transport, out []Report, in map[byte]int, log func(string)) (Battery, error) {
	c := &Conn{T: t, Out: out, In: in, Log: log}
	framing := func(sync bool) string {
		if sync {
			return "with sync byte"
		}
		return "without sync byte"
	}
	var lastErr error = ErrTimeout
	for _, sync := range []bool{false, true} {
		c.Sync = sync
		c.logf("IdentifyDeviceLingoes, %s", framing(sync))
		st, err := c.IdentifyLingoes()
		if errors.Is(err, ErrTimeout) {
			c.logf("   no answer")
			continue
		}
		if err != nil {
			return Battery{}, err
		}
		if st != 0 {
			c.logf("   refused with status 0x%02X; trying the older Identify", st)
			if err := c.IdentifyLegacy(); err != nil {
				return Battery{}, err
			}
		}
		b, err := c.PowerBattery()
		if err == nil {
			c.logf("battery: state 0x%02X, level %d/255", b.State, b.Level)
			return b, nil
		}
		c.logf("   battery request failed: %v", err)
		return Battery{}, err // the iPod is talking; a second framing won't help
	}
	for _, sync := range []bool{false, true} {
		c.Sync = sync
		c.logf("older Identify, %s", framing(sync))
		if err := c.IdentifyLegacy(); err != nil {
			return Battery{}, err
		}
		b, err := c.PowerBattery()
		if err == nil {
			c.logf("battery: state 0x%02X, level %d/255", b.State, b.Level)
			return b, nil
		}
		c.logf("   battery request failed: %v", err)
		lastErr = err
	}
	return Battery{}, lastErr
}
