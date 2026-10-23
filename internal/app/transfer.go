package app

import (
	"io"
	"os"
	"sync"
	"time"
)

// Transfer speed: file copies to and from the iPod report the bytes they
// move, and progress events carry the speed (over the last few seconds),
// the bytes done and, when known, the bytes in total.

type xferSample struct {
	at    time.Time
	bytes int64
}

type transfer struct {
	mu       sync.Mutex
	a        *App
	job      string
	done     int // items, as last reported by progress
	total    int
	label    string
	bytes    int64 // copied so far
	total64  int64 // expected, 0 if unknown
	dir      string
	samples  []xferSample
	lastEmit time.Time
}

const speedWindow = 3 * time.Second

func (x *transfer) speed(now time.Time) float64 {
	// drop samples outside the window, keeping one just before it
	for len(x.samples) > 2 && now.Sub(x.samples[1].at) > speedWindow {
		x.samples = x.samples[1:]
	}
	if len(x.samples) < 2 {
		return 0
	}
	first, last := x.samples[0], x.samples[len(x.samples)-1]
	if now.Sub(last.at) > 2*time.Second { // nothing moving at the moment
		return 0
	}
	dt := last.at.Sub(first.at).Seconds()
	if dt < 0.2 {
		return 0
	}
	return float64(last.bytes-first.bytes) / dt
}

func (x *transfer) event(now time.Time) Event {
	e := Event{Type: "progress", Job: x.job, Done: x.done, Total: x.total, Label: x.label,
		BytesDone: x.bytes, BytesTotal: x.total64, Direction: x.dir}
	e.Speed = x.speed(now)
	return e
}

// moved records n more bytes copied, sending a progress event at most
// four times a second.
func (x *transfer) moved(n int64) {
	if x == nil {
		return
	}
	x.mu.Lock()
	now := time.Now()
	x.bytes += n
	if len(x.samples) == 0 {
		x.samples = append(x.samples, xferSample{now, x.bytes - n})
	}
	x.samples = append(x.samples, xferSample{now, x.bytes})
	if len(x.samples) > 400 {
		x.samples = x.samples[len(x.samples)-200:]
	}
	var e *Event
	if now.Sub(x.lastEmit) >= 250*time.Millisecond {
		x.lastEmit = now
		ev := x.event(now)
		e = &ev
	}
	x.mu.Unlock()
	if e != nil {
		x.a.emit(*e)
	}
}

// startTransfer begins byte counting for the running job. dir is "to"
// (to the iPod) or "from".
func (a *App) startTransfer(job, dir string, totalBytes int64) *transfer {
	x := &transfer{a: a, job: job, dir: dir, total64: totalBytes}
	a.mu.Lock()
	a.xfer = x
	a.mu.Unlock()
	return x
}

func (a *App) currentTransfer() *transfer {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.xfer
}

// copyFileCounted copies a file, reporting bytes to x (which may be nil).
// dst must not exist.
func copyFileCounted(src, dst string, x *transfer) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	buf := make([]byte, 1<<20)
	for {
		n, rerr := in.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				out.Close()
				return werr
			}
			x.moved(int64(n))
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return rerr
		}
	}
	return out.Close()
}
