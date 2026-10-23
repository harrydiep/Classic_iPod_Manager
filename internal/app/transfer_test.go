package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTransferSpeed(t *testing.T) {
	x := &transfer{}
	t0 := time.Now()
	for i := 0; i <= 10; i++ { // 10 MB over one second
		x.samples = append(x.samples, xferSample{t0.Add(time.Duration(i) * 100 * time.Millisecond), int64(i) << 20})
	}
	if s := x.speed(t0.Add(time.Second)); s < 9.9*(1<<20) || s > 10.1*(1<<20) {
		t.Fatalf("speed %.0f", s)
	}
	if s := x.speed(t0.Add(5 * time.Second)); s != 0 { // stalled
		t.Fatalf("stalled speed %.0f", s)
	}
}

func TestCopyReportsBytes(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.bin")
	data := bytes.Repeat([]byte("x"), 3<<20+123)
	os.WriteFile(src, data, 0o644)
	var events []Event
	a := New(&fakePlat{}, "", func(e Event) { events = append(events, e) })
	x := a.startTransfer("add", "to", int64(len(data)))
	if err := copyFileCounted(src, filepath.Join(dir, "b.bin"), x); err != nil {
		t.Fatal(err)
	}
	a.progress("add", 1, 1, "done")
	last := events[len(events)-1]
	if last.BytesDone != int64(len(data)) || last.BytesTotal != int64(len(data)) || last.Direction != "to" || last.Label != "done" {
		t.Fatalf("last event %+v", last)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.bin")); !bytes.Equal(b, data) {
		t.Fatal("copy differs")
	}
	if err := copyFileCounted(src, filepath.Join(dir, "b.bin"), nil); err == nil {
		t.Fatal("overwrote an existing file")
	}
}
