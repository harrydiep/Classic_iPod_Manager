package convert

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"podmanager/internal/media"
)

// ErrNoFFmpeg means a video needs converting and no ffmpeg.exe was found.
type ErrNoFFmpeg struct{ Dir string }

func (e ErrNoFFmpeg) Error() string {
	return "this video needs converting for the iPod – put ffmpeg.exe in " + e.Dir
}

var (
	encMu    sync.Mutex
	encCache = map[string]string{} // ffmpeg path → output of -encoders
)

func ffmpegHas(t *Tool, encoder string) bool {
	encMu.Lock()
	out, ok := encCache[t.Path]
	encMu.Unlock()
	if !ok {
		cmd := exec.Command(t.Path, "-hide_banner", "-encoders")
		hideWindow(cmd)
		b, _ := cmd.Output()
		out = string(b)
		encMu.Lock()
		encCache[t.Path] = out
		encMu.Unlock()
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == encoder {
			return true
		}
	}
	return false
}

// ConvertVideo makes an iPod-compatible .m4v from a video with FFmpeg:
// H.264 Baseline (or MPEG-4 when FFmpeg has no H.264 encoder) up to
// 640×480 and 30 frames a second, with stereo AAC sound. Streams that are
// already fine are copied. progress (0..1) is called as it goes.
func (c Config) ConvertVideo(ctx context.Context, src string, vi *media.VideoInfo, out string, progress func(float64)) error {
	ff := c.ffmpeg()
	if ff == nil {
		return ErrNoFFmpeg{c.Dir("decoder")}
	}
	videoOK := vi.Remux || vi.Playable
	audioOK := vi.AudioCodec == "aac" && vi.Container != "other"
	args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", src,
		"-map", "0:v:0", "-map", "0:a:0?", "-sn", "-dn", "-map_metadata", "0", "-map_chapters", "-1"}
	if videoOK && vi.Container != "other" {
		args = append(args, "-c:v", "copy")
	} else {
		fit := "scale='min(640,iw)':'min(480,ih)':force_original_aspect_ratio=decrease,scale=trunc(iw/2)*2:trunc(ih/2)*2,setsar=1"
		if ffmpegHas(ff, "libx264") {
			args = append(args, "-vf", fit, "-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0", "-pix_fmt", "yuv420p",
				"-preset", "medium", "-b:v", "1200k", "-maxrate", "1500k", "-bufsize", "2000k", "-fpsmax", "30")
		} else {
			fit = "scale='min(480,iw)':'min(480,ih)':force_original_aspect_ratio=decrease,scale=trunc(iw/2)*2:trunc(ih/2)*2,setsar=1"
			args = append(args, "-vf", fit, "-c:v", "mpeg4", "-vtag", "mp4v", "-b:v", "1800k", "-maxrate", "2400k",
				"-bufsize", "2400k", "-fpsmax", "30")
		}
	}
	if audioOK {
		args = append(args, "-c:a", "copy")
	} else {
		args = append(args, "-c:a", "aac", "-b:a", "128k", "-ac", "2", "-ar", "44100")
	}
	args = append(args, "-movflags", "+faststart", "-f", "mp4")
	if vi.LengthMs == 0 {
		vi.LengthMs = ffmpegDuration(ctx, ff, src)
	}
	work := filepath.Dir(out)
	tmp := out + ".part"
	err := runFFmpegProgress(ctx, ff, work, append(args, "-progress", "pipe:1", "-nostats", tmp), vi.LengthMs, progress)
	if err != nil && strings.Contains(err.Error(), "fpsmax") {
		// FFmpeg older than 4.3: no -fpsmax
		for i, a := range args {
			if a == "-fpsmax" {
				args = append(args[:i:i], args[i+2:]...)
				break
			}
		}
		err = runFFmpegProgress(ctx, ff, work, append(args, "-progress", "pipe:1", "-nostats", tmp), vi.LengthMs, progress)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	check, err := media.ProbeVideo(tmp)
	if err != nil || !check.Playable {
		os.Remove(tmp)
		if err == nil {
			err = errors.New(check.Reason)
		}
		return fmt.Errorf("the converted video still isn't playable on an iPod: %w", err)
	}
	return os.Rename(tmp, out)
}

// runFFmpegProgress runs FFmpeg with -progress pipe:1 and reports how far
// it has got.
func runFFmpegProgress(ctx context.Context, t *Tool, dir string, args []string, lengthMs uint32, progress func(float64)) error {
	cmd := exec.CommandContext(ctx, t.Path, args...)
	cmd.Dir = dir
	hideWindow(cmd)
	cmd.WaitDelay = 3 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitWriter{w: &stderr, n: 64 << 10}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("couldn't start %s: %w", t.Name, err)
	}
	sc := bufio.NewScanner(pipe)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || progress == nil || lengthMs == 0 {
			continue
		}
		if k == "out_time_us" || k == "out_time_ms" { // both are microseconds
			if us, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && us > 0 {
				f := float64(us) / 1000 / float64(lengthMs)
				if f > 1 {
					f = 1
				}
				progress(f)
			}
		}
	}
	io.Copy(io.Discard, pipe)
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(lastLines(stderr.String(), 3))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s failed: %s", t.Name, msg)
	}
	return nil
}

// VideoThumbnail grabs a frame about a tenth of the way into a video as a
// JPEG, for the iPod's video lists. Returns nil without FFmpeg.
func (c Config) VideoThumbnail(ctx context.Context, src string, lengthMs uint32) []byte {
	ff := c.ffmpeg()
	if ff == nil {
		return nil
	}
	at := float64(lengthMs) / 10000
	if at > 60 {
		at = 60
	}
	cmd := exec.CommandContext(ctx, ff.Path, "-nostdin", "-hide_banner", "-loglevel", "error", "-ss", strconv.FormatFloat(at, 'f', 2, 64),
		"-i", src, "-frames:v", "1", "-vf", "scale=400:-2", "-q:v", "3", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
	hideWindow(cmd)
	cmd.WaitDelay = 3 * time.Second
	out, err := cmd.Output()
	if err != nil || len(out) < 100 {
		return nil
	}
	return out
}

// ffmpegDuration asks FFmpeg how long a file is (for files PodManager can't
// read itself), in milliseconds; 0 if unknown.
func ffmpegDuration(ctx context.Context, ff *Tool, src string) uint32 {
	cmd := exec.CommandContext(ctx, ff.Path, "-nostdin", "-hide_banner", "-i", src)
	hideWindow(cmd)
	cmd.WaitDelay = 3 * time.Second
	var b bytes.Buffer
	cmd.Stderr = &limitWriter{w: &b, n: 256 << 10}
	cmd.Run() // exits with an error: no output file
	out := b.String()
	i := strings.Index(out, "Duration: ")
	if i < 0 {
		return 0
	}
	v := out[i+10:]
	if j := strings.IndexByte(v, ','); j > 0 {
		v = v[:j]
	}
	var h, m int
	var sec float64
	if n, _ := fmt.Sscanf(strings.TrimSpace(v), "%d:%d:%f", &h, &m, &sec); n != 3 {
		return 0
	}
	return uint32((float64(h*3600+m*60) + sec) * 1000)
}

// VideoLength returns a video's length using FFmpeg when PodManager can't
// read the file itself; 0 if unknown.
func (c Config) VideoLength(ctx context.Context, src string) uint32 {
	if ff := c.ffmpeg(); ff != nil {
		return ffmpegDuration(ctx, ff, src)
	}
	return 0
}
