package iap

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestEncodeDecode(t *testing.T) {
	p := Encode(0x00, 0x13, []byte{0, 0, 0, 9, 0, 0, 0, 0, 0, 0, 0, 0})
	if p[0] != 0x55 || p[1] != 14 || p[2] != 0 || p[3] != 0x13 {
		t.Fatalf("header % X", p)
	}
	var sum byte
	for _, b := range p[1:] {
		sum += b
	}
	if sum != 0 {
		t.Fatal("checksum does not zero the sum")
	}
	// padding before and after must be ignored
	in := append([]byte{0, 0}, p...)
	in = append(in, 0, 0, 0)
	got, used, ok, err := Decode(in)
	if err != nil || !ok || used != 2+len(p) || got.Lingo != 0 || got.Cmd != 0x13 || len(got.Data) != 12 {
		t.Fatalf("decode %+v %d %v %v", got, used, ok, err)
	}
	if _, _, ok, _ := Decode(p[:len(p)-1]); ok {
		t.Fatal("partial packet decoded")
	}
	bad := append([]byte(nil), p...)
	bad[len(bad)-1]++
	if _, _, _, err := Decode(bad); err == nil {
		t.Fatal("bad checksum accepted")
	}
	// large packet form
	big := Encode(3, 1, bytes.Repeat([]byte{7}, 300))
	if big[1] != 0 || int(big[2])<<8|int(big[3]) != 302 {
		t.Fatalf("large header % X", big[:4])
	}
	if got, _, ok, err := Decode(big); !ok || err != nil || len(got.Data) != 300 {
		t.Fatal("large packet round trip")
	}
}

func TestFrame(t *testing.T) {
	pkt := Encode(0, 0x13, make([]byte, 12)) // 17 bytes
	reps, err := Frame(pkt, []Report{{ID: 2, Size: 9}, {ID: 1, Size: 5}, {ID: 3, Size: 63}})
	if err != nil || len(reps) != 1 || reps[0][0] != 3 || reps[0][1] != 0 || len(reps[0]) != 64 {
		t.Fatalf("single report: %v %v", reps, err)
	}
	reps, _ = Frame(pkt, []Report{{ID: 1, Size: 5}, {ID: 2, Size: 9}})
	// 17 bytes in chunks of 8: 8 + 8 + 1 (the last fits in report 1)
	if len(reps) != 3 || reps[0][1] != linkMoreToFollow || reps[1][1] != linkMoreToFollow|linkContinuation || reps[2][1] != linkContinuation || reps[2][0] != 1 {
		t.Fatalf("fragments: % X", reps)
	}
	var joined []byte
	for i, r := range reps {
		n := len(r) - 2
		if i == len(reps)-1 {
			n = len(pkt) - len(joined)
		}
		joined = append(joined, r[2:2+n]...)
	}
	if !bytes.Equal(joined, pkt) {
		t.Fatal("fragments don't rebuild the packet")
	}
}

// fakePod answers like an iPod over HID with small reports.
type fakePod struct {
	t        *testing.T
	mode     string // "modern", "statinfo", "ancient", "silent"
	needSync bool   // ignore packets without the 0xFF sync byte
	battery  Battery
	inSize   int
	rx       []byte
	queue    [][]byte
	sent     []Packet
	identify bool
}

func (f *fakePod) Write(rep []byte) error {
	if rep[1]&linkContinuation == 0 {
		f.rx = f.rx[:0]
	}
	f.rx = append(f.rx, rep[2:]...)
	if rep[1]&linkMoreToFollow != 0 {
		return nil
	}
	p, _, ok, err := Decode(f.rx)
	if !ok || err != nil {
		f.t.Fatalf("pod got a bad packet % X: %v", f.rx, err)
	}
	if f.needSync && f.rx[0] != 0xFF {
		return nil
	}
	f.sent = append(f.sent, p)
	f.handle(p)
	return nil
}

func (f *fakePod) reply(lingo, cmd byte, data []byte) {
	pkt := Encode(lingo, cmd, data)
	for off := 0; off < len(pkt); off += f.inSize - 1 {
		end := off + f.inSize - 1
		var link byte
		if off > 0 {
			link |= linkContinuation
		}
		if end < len(pkt) {
			link |= linkMoreToFollow
		} else {
			end = len(pkt)
		}
		rep := make([]byte, 1+f.inSize)
		rep[0], rep[1] = 0x05, link
		copy(rep[2:], pkt[off:end])
		f.queue = append(f.queue, rep)
	}
}

func (f *fakePod) handle(p Packet) {
	if f.mode == "silent" {
		return
	}
	switch {
	case p.Lingo == 0 && p.Cmd == cmdIdentifyDeviceLingoes:
		if f.mode == "ancient" {
			return // unknown command, no answer
		}
		f.identify = true
		f.reply(0, cmdGeneralAck, []byte{0, cmdIdentifyDeviceLingoes})
	case p.Lingo == 0 && p.Cmd == cmdIdentify:
		f.identify = true
	case p.Lingo == 3 && p.Cmd == cmdGetPowerBatteryState:
		// an unrelated notification arrives first
		f.reply(3, 0x09, []byte{0x01, 0, 0, 0, 5})
		if f.mode == "modern" {
			f.reply(3, cmdRetPowerBatteryState, []byte{f.battery.State, f.battery.Level})
		} else if f.mode == "statinfo" {
			f.reply(3, cmdRemoteAck, []byte{0x05, cmdGetPowerBatteryState})
		}
	case p.Lingo == 3 && p.Cmd == cmdGetiPodStateInfo && p.Data[0] == stateInfoPowerBattery:
		f.reply(3, cmdRetiPodStateInfo, []byte{stateInfoPowerBattery, f.battery.State, f.battery.Level})
	}
}

func (f *fakePod) Read(timeout time.Duration) ([]byte, error) {
	if len(f.queue) == 0 {
		return nil, ErrTimeout
	}
	r := f.queue[0]
	f.queue = f.queue[1:]
	return r, nil
}

func TestQueryBattery(t *testing.T) {
	out := []Report{{ID: 1, Size: 5}, {ID: 2, Size: 9}}
	for _, mode := range []string{"modern", "statinfo", "ancient"} {
		pod := &fakePod{t: t, mode: mode, battery: Battery{State: StateCharging, Level: 184}, inSize: 6}
		b, err := QueryBattery(pod, out, map[byte]int{5: 6}, nil)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		if !pod.identify || b.State != StateCharging || b.Level != 184 || b.Percent() != 72 || !b.Charging() {
			t.Fatalf("%s: %+v %d", mode, b, b.Percent())
		}
	}
	pod := &fakePod{t: t, mode: "silent", inSize: 6}
	if _, err := QueryBattery(pod, out, nil, nil); err == nil {
		t.Fatal("silent iPod should time out")
	}
	// firmware that only listens to framing with the sync byte
	var log []string
	pod = &fakePod{t: t, mode: "modern", needSync: true, battery: Battery{State: StateBattery, Level: 255}, inSize: 6}
	b, err := QueryBattery(pod, out, nil, func(s string) { log = append(log, s) })
	if err != nil || b.Percent() != 100 {
		t.Fatalf("sync framing: %+v %v\n%s", b, err, strings.Join(log, "\n"))
	}
	if len(log) < 6 || !strings.Contains(strings.Join(log, "\n"), "with sync byte") {
		t.Fatalf("log:\n%s", strings.Join(log, "\n"))
	}
	// an ancient iPod that also wants the sync byte
	pod = &fakePod{t: t, mode: "ancient", needSync: true, battery: Battery{State: StateCharging, Level: 10}, inSize: 6}
	if b, err := QueryBattery(pod, out, nil, nil); err != nil || b.Level != 10 {
		t.Fatalf("ancient sync: %+v %v", b, err)
	}
}

func TestPercent(t *testing.T) {
	cases := []struct {
		b    Battery
		want int
	}{
		{Battery{StateBattery, 255}, 100},
		{Battery{StateBatteryLow, 30}, 12},
		{Battery{StateCharging, 0}, -1},
		{Battery{StateCharged, 0}, 100},
		{Battery{StateBattery, 0}, 0},
	}
	for _, c := range cases {
		if got := c.b.Percent(); got != c.want {
			t.Errorf("%+v: %d, want %d", c.b, got, c.want)
		}
	}
}
