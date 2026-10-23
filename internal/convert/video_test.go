package convert

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"

	"podmanager/internal/media"
)

// makeVideo renders a short test video with FFmpeg.
func makeVideo(t *testing.T, path, size string, args ...string) {
	t.Helper()
	a := []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=30:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=44100", "-shortest"}
	a = append(a, args...)
	a = append(a, "-y", path)
	if b, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", filepath.Base(path), err, b)
	}
}

func TestVideoProbeAndConvert(t *testing.T) {
	need(t, "ffmpeg")
	dir := t.TempDir()
	cases := []struct {
		name, size     string
		args           []string
		playable, remx bool
	}{
		{"ok.m4v", "320x240", []string{"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "128k"}, true, false},
		{"mpeg4.mp4", "480x360", []string{"-c:v", "mpeg4", "-b:v", "1000k", "-c:a", "aac"}, true, false},
		{"hd.mp4", "1280x720", []string{"-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p", "-c:a", "aac"}, false, false},
		{"base.mov", "320x240", []string{"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0", "-pix_fmt", "yuv420p", "-c:a", "aac"}, false, true},
		{"clip.mkv", "640x360", []string{"-c:v", "libx264", "-profile:v", "baseline", "-pix_fmt", "yuv420p", "-c:a", "libvorbis"}, false, false},
		{"old.avi", "320x240", []string{"-c:v", "mpeg4", "-c:a", "libmp3lame"}, false, false},
		{"mp3sound.mp4", "320x240", []string{"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0", "-pix_fmt", "yuv420p", "-c:a", "libmp3lame"}, false, false},
	}
	cfg := Config{Main: filepath.Join(dir, "Main")}
	link(t, filepath.Join(dir, "Main", "Encoders", "Decoder"), "ffmpeg")
	for _, c := range cases {
		p := filepath.Join(dir, c.name)
		makeVideo(t, p, c.size, c.args...)
		if !media.IsVideo(p) {
			t.Fatalf("%s: not seen as a video", c.name)
		}
		vi, err := media.ProbeVideo(p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if vi.Playable != c.playable || vi.Remux != c.remx {
			t.Fatalf("%s: playable=%v remux=%v reason=%q %+v", c.name, vi.Playable, vi.Remux, vi.Reason, vi)
		}
		if vi.Container != "other" && (vi.LengthMs < 2900 || vi.LengthMs > 3200 || vi.Width == 0) {
			t.Fatalf("%s: %+v", c.name, vi)
		}
		t.Logf("%-14s %s %s %d×%d p%d l%d %d kbps: %s", c.name, vi.Container, vi.VideoCodec, vi.Width, vi.Height, vi.Profile, vi.Level, vi.Bitrate, vi.Reason)
		if c.playable {
			continue
		}
		out := filepath.Join(dir, "out-"+c.name+".m4v")
		var last float64
		if err := cfg.ConvertVideo(context.Background(), p, vi, out, func(f float64) { last = f }); err != nil {
			t.Fatalf("%s: convert: %v", c.name, err)
		}
		cv, err := media.ProbeVideo(out)
		if err != nil || !cv.Playable || cv.Width > 640 || cv.Height > 480 || cv.LengthMs < 2800 || cv.AudioCodec != "aac" {
			t.Fatalf("%s: converted: %+v %v", c.name, cv, err)
		}
		if last < 0.5 {
			t.Errorf("%s: progress only reached %.2f", c.name, last)
		}
		t.Logf("   → %s %d×%d p%d l%d %d kbps", cv.VideoCodec, cv.Width, cv.Height, cv.Profile, cv.Level, cv.Bitrate)
	}
	// a song in an .mp4 isn't a video
	song := filepath.Join(dir, "song.mp4")
	if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=duration=2", "-c:a", "aac", "-y", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	if media.IsVideo(song) {
		t.Fatal("an audio-only .mp4 was taken for a video")
	}
	// a thumbnail
	if jpg := cfg.VideoThumbnail(context.Background(), filepath.Join(dir, "ok.m4v"), 3000); len(jpg) < 1000 || jpg[0] != 0xFF || jpg[1] != 0xD8 {
		t.Fatalf("thumbnail: %d bytes", len(jpg))
	}
	// no FFmpeg: a clear error
	vi, _ := media.ProbeVideo(filepath.Join(dir, "hd.mp4"))
	err := Config{Main: t.TempDir()}.ConvertVideo(context.Background(), filepath.Join(dir, "hd.mp4"), vi, filepath.Join(dir, "x.m4v"), nil)
	if _, ok := err.(ErrNoFFmpeg); !ok {
		t.Fatalf("want ErrNoFFmpeg, got %v", err)
	}
}
