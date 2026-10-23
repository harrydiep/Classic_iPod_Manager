package convert

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"podmanager/internal/media"
)

func need(t *testing.T, tools ...string) {
	for _, n := range tools {
		if _, err := exec.LookPath(n); err != nil {
			t.Skipf("%s not installed", n)
		}
	}
}

// link puts a program into an encoder folder under its usual name.
func link(t *testing.T, dir, name string) {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	os.MkdirAll(dir, 0o755)
	if err := os.Symlink(p, filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}
}

func tone(t *testing.T, path string, args ...string) {
	t.Helper()
	a := append([]string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=330:duration=3:sample_rate=" + sampleRate(args), "-ac", "2"}, filterRate(args)...)
	a = append(a, "-y", path)
	if b, err := exec.Command("ffmpeg", a...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
}

// sampleRate/filterRate let a test pass "rate=96000" as the first argument.
func sampleRate(args []string) string {
	if len(args) > 0 && len(args[0]) > 5 && args[0][:5] == "rate=" {
		return args[0][5:]
	}
	return "44100"
}
func filterRate(args []string) []string {
	if len(args) > 0 && len(args[0]) > 5 && args[0][:5] == "rate=" {
		return args[1:]
	}
	return args
}

func cover() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 120))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(0, 0, color.RGBA{1, 2, 3, 255})
	var b bytes.Buffer
	jpeg.Encode(&b, img, nil)
	return b.Bytes()
}

type srcFile struct{ path string }

func sources(t *testing.T, dir string) map[string]string {
	need(t, "ffmpeg")
	s := map[string]string{
		"flac24": filepath.Join(dir, "Hï-Rés Ünïcode.flac"),
		"mp3":    filepath.Join(dir, "song.mp3"),
		"ogg":    filepath.Join(dir, "song.ogg"),
		"wav24":  filepath.Join(dir, "song24.wav"),
		"aiff":   filepath.Join(dir, "song.aiff"),
		"m4a":    filepath.Join(dir, "song.m4a"),
	}
	tone(t, s["flac24"], "rate=96000", "-c:a", "flac", "-sample_fmt", "s32")
	tone(t, s["mp3"], "-c:a", "libmp3lame", "-b:a", "192k")
	tone(t, s["ogg"], "-c:a", "libvorbis")
	tone(t, s["wav24"], "-c:a", "pcm_s24le")
	tone(t, s["aiff"], "-c:a", "pcm_s16be")
	tone(t, s["m4a"], "-c:a", "aac")
	if err := media.WriteTagsArt(s["flac24"], media.Tags{Title: "Source Title"}, media.ArtEdit{Set: &media.Picture{Data: cover()}}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConvertMatrix(t *testing.T) {
	need(t, "ffmpeg", "lame", "flac", "oggenc", "fdkaac")
	dir := t.TempDir()
	src := sources(t, dir)
	main := filepath.Join(dir, "Main")
	cfgDedicated := Config{Main: main, Folders: map[string]string{
		"mp3": "enc-a/mp3", "aac": "enc-a/aac", "alac": "enc-a/alac", "flac": "enc-a/flac", "vorbis": "enc-a/ogg", "decoder": "enc-a/dec"}}
	link(t, filepath.Join(main, "enc-a/mp3"), "lame")
	link(t, filepath.Join(main, "enc-a/aac"), "fdkaac")
	link(t, filepath.Join(main, "enc-a/alac/ffmpeg-7.1"), "ffmpeg") // one level down is found too
	link(t, filepath.Join(main, "enc-a/flac"), "flac")
	link(t, filepath.Join(main, "enc-a/ogg"), "oggenc")
	link(t, filepath.Join(main, "enc-a/ogg"), "oggdec")
	// FFmpeg for everything
	cfgFF := Config{Main: main, Folders: map[string]string{"mp3": "enc-b", "aac": "enc-b", "alac": "enc-b", "flac": "enc-b", "vorbis": "enc-b", "decoder": "enc-b"}}
	link(t, filepath.Join(main, "enc-b"), "ffmpeg")

	tags := media.Tags{Title: "Converted", Artist: "Ärtist", Album: "Album", Track: 2, TrackTotal: 5, Year: 2001}
	type tc struct {
		name, src, format, quality string
		cfg                        Config
		forIPod                    bool
		wantBits                   int
		wantRate                   uint32
		wantKbps                   uint32 // rough
	}
	cases := []tc{
		{"flac24→mp3 lame", "flac24", "mp3", "cbr192", cfgDedicated, true, 0, 48000, 192},
		{"flac24→mp3 V2", "flac24", "mp3", "v2", cfgDedicated, true, 0, 48000, 0},
		{"wav24→aac fdkaac", "wav24", "aac", "cbr128", cfgDedicated, true, 0, 44100, 128},
		{"mp3→alac ffmpeg (sub-folder)", "mp3", "alac", "", cfgDedicated, true, 16, 44100, 0},
		{"flac24→alac iPod", "flac24", "alac", "", cfgDedicated, true, 16, 48000, 0},
		{"flac24→alac keep", "flac24", "alac", "", cfgFF, false, 24, 96000, 0},
		{"mp3→flac flac.exe", "mp3", "flac", "l8", cfgDedicated, false, 16, 44100, 0},
		{"wav24→flac keep 24", "wav24", "flac", "l5", cfgDedicated, false, 24, 44100, 0},
		{"aiff→vorbis oggenc", "aiff", "vorbis", "q6", cfgDedicated, false, 0, 44100, 0},
		{"ogg→wav (oggdec)", "ogg", "wav", "", cfgDedicated, true, 16, 44100, 0},
		{"m4a→aiff ffmpeg", "m4a", "aiff", "", cfgFF, true, 16, 44100, 0},
		{"wav24→wav iPod 16", "wav24", "wav", "", cfgDedicated, true, 16, 44100, 0},
		{"flac24→aac ffmpeg", "flac24", "aac", "cbr256", cfgFF, true, 0, 48000, 256},
		{"flac24→vorbis ffmpeg", "flac24", "vorbis", "q5", cfgFF, false, 0, 96000, 0},
		{"mp3→mp3 ffmpeg 320", "mp3", "mp3", "cbr320", cfgFF, true, 0, 44100, 320},
	}
	for i, c := range cases {
		info, err := media.Inspect(src[c.src])
		if err != nil {
			t.Fatal(err)
		}
		f, _ := FormatByID(c.format)
		out := filepath.Join(dir, "out", string(rune('a'+i))+f.Ext)
		start := time.Now()
		err = c.cfg.Run(context.Background(), Job{Src: src[c.src], Info: info, Format: c.format, Quality: c.quality,
			ForIPod: c.forIPod, Tags: tags, Out: out, WorkDir: dir})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		si, err := media.Inspect(out)
		if err != nil {
			t.Fatalf("%s: output: %v", c.name, err)
		}
		wantFormat := map[string]string{"mp3": "mp3", "aac": "aac", "alac": "alac", "flac": "flac", "vorbis": "vorbis", "wav": "wav", "aiff": "aiff"}[c.format]
		if si.Format != wantFormat || si.SampleRate != c.wantRate || (c.wantBits > 0 && si.Bits != c.wantBits) ||
			si.LengthMs < 2900 || si.LengthMs > 3200 {
			t.Fatalf("%s: got %+v", c.name, si)
		}
		if c.wantKbps > 0 && (si.Bitrate < c.wantKbps*8/10 || si.Bitrate > c.wantKbps*12/10) {
			t.Errorf("%s: bitrate %d, want about %d", c.name, si.Bitrate, c.wantKbps)
		}
		if f.Tags {
			if si.Tags.Title != "Converted" || si.Tags.Artist != "Ärtist" || si.Tags.Track != 2 || si.Tags.Year != 2001 {
				t.Errorf("%s: tags %+v", c.name, si.Tags)
			}
			// art is carried over from the FLAC source
			if c.src == "flac24" && !si.HasArt {
				t.Errorf("%s: art not carried over", c.name)
			}
		}
		if si.Playable != f.IPod {
			t.Errorf("%s: playable %v", c.name, si.Playable)
		}
		t.Logf("%-30s %s %d kbps %d Hz %d-bit (%v)", c.name, si.Label, si.Bitrate, si.SampleRate, si.Bits, time.Since(start).Round(time.Millisecond))
	}
	// no temp folders left behind
	left, _ := filepath.Glob(filepath.Join(dir, "podmanager-convert-*"))
	if len(left) > 0 {
		t.Fatalf("temp folders left: %v", left)
	}
}

func TestConvertErrors(t *testing.T) {
	need(t, "ffmpeg")
	dir := t.TempDir()
	src := sources(t, dir)
	cfg := Config{Main: filepath.Join(dir, "Main")}
	info, _ := media.Inspect(src["mp3"])
	err := cfg.Run(context.Background(), Job{Src: src["mp3"], Info: info, Format: "aac", Out: filepath.Join(dir, "x.m4a"), WorkDir: dir})
	var ne ErrNoEncoder
	if !errors.As(err, &ne) || ne.Format.ID != "aac" {
		t.Fatalf("want ErrNoEncoder, got %v", err)
	}
	// FLAC source, WAV output (built in), but nothing that reads FLAC
	finfo, _ := media.Inspect(src["flac24"])
	err = cfg.Run(context.Background(), Job{Src: src["flac24"], Info: finfo, Format: "wav", Out: filepath.Join(dir, "x.wav"), WorkDir: dir})
	if err == nil {
		t.Fatal("expected a decoder error")
	}
	if cfg.Check(Job{Src: src["flac24"], Info: finfo, Format: "wav"}) == nil {
		t.Fatal("Check should fail too")
	}
	// PCM to PCM needs nothing
	winfo, _ := media.Inspect(src["wav24"])
	out := filepath.Join(dir, "y.aif")
	if err := cfg.Run(context.Background(), Job{Src: src["wav24"], Info: winfo, Format: "aiff", ForIPod: true, Out: out, WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	if si, err := media.Inspect(out); err != nil || si.Bits != 16 || si.Format != "aiff" {
		t.Fatalf("aiff: %+v %v", si, err)
	}
	// ffmpeg confirms the built-in AIFF decodes cleanly
	if b, err := exec.Command("ffmpeg", "-v", "error", "-i", out, "-f", "null", "-").CombinedOutput(); err != nil || len(b) > 0 {
		t.Fatalf("aiff decode: %v %s", err, b)
	}
	// a failing encoder reports its message
	bad := filepath.Join(dir, "Main", "bad")
	os.MkdirAll(bad, 0o755)
	os.WriteFile(filepath.Join(bad, "lame"), []byte("#!/bin/sh\necho 'Unsupported option: banana' >&2\nexit 1\n"), 0o755)
	cfg.Folders = map[string]string{"mp3": "bad"}
	err = cfg.Run(context.Background(), Job{Src: src["wav24"], Info: winfo, Format: "mp3", Out: filepath.Join(dir, "z.mp3"), WorkDir: dir})
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("banana")) {
		t.Fatalf("encoder error: %v", err)
	}
	// cancelling stops a running encoder
	os.WriteFile(filepath.Join(bad, "lame"), []byte("#!/bin/sh\nsleep 30\n"), 0o755)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(300 * time.Millisecond); cancel() }()
	start := time.Now()
	err = cfg.Run(ctx, Job{Src: src["wav24"], Info: winfo, Format: "mp3", Out: filepath.Join(dir, "z.mp3"), WorkDir: dir})
	if !errors.Is(err, context.Canceled) || time.Since(start) > 5*time.Second {
		t.Fatalf("cancel: %v after %v", err, time.Since(start))
	}
}

func TestStatuses(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{Main: dir}
	if err := cfg.MakeFolders(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "Encoders", "Apple Lossless", "Put the encoder here.txt")); err != nil {
		t.Fatal(err)
	}
	link(t, filepath.Join(dir, "Encoders", "MP3"), "lame")
	link(t, filepath.Join(dir, "Encoders", "MP3"), "ffmpeg")
	cfg.Choice = map[string]string{"mp3": "ffmpeg"}
	st := cfg.Statuses()
	byID := map[string]Status{}
	for _, s := range st {
		byID[s.ID] = s
	}
	if s := byID["mp3"]; !s.Exists || len(s.Tools) != 2 || s.Active != "ffmpeg" {
		t.Fatalf("mp3 status %+v", s)
	}
	// AAC has nothing of its own, so it uses the FFmpeg from the MP3 folder
	if s := byID["aac"]; s.Active != "ffmpeg" || s.From != "MP3" || !s.Exists {
		t.Fatalf("aac status %+v", s)
	}
	if s := byID["wav"]; s.Active != "Built in" {
		t.Fatalf("wav status %+v", s)
	}
	// the decoder uses the FFmpeg from the MP3 folder
	if s := byID["decoder"]; s.Active == "" {
		t.Fatalf("decoder status %+v", s)
	}
	if d := cfg.Dir("flac"); d != filepath.Join(dir, "Encoders", "FLAC") {
		t.Fatalf("dir %s", d)
	}
	cfg.Folders = map[string]string{"flac": "/opt/flac"}
	if d := cfg.Dir("flac"); d != "/opt/flac" {
		t.Fatalf("absolute dir %s", d)
	}
}

// A multi-format program in one folder covers formats whose own folder is
// empty; a program in a format's own folder still wins.
func TestBorrowedEncoders(t *testing.T) {
	need(t, "ffmpeg")
	dir := t.TempDir()
	cfg := Config{Main: dir}
	cfg.MakeFolders()
	link(t, filepath.Join(dir, "Encoders", "AAC"), "ffmpeg")
	link(t, filepath.Join(dir, "Encoders", "MP3"), "lame")
	for _, id := range []string{"alac", "flac", "vorbis", "aac", "mp3"} {
		tool, err := cfg.Encoder(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		want, from := "ffmpeg", "AAC"
		switch id {
		case "aac":
			from = ""
		case "mp3":
			want, from = "lame", ""
		}
		if tool.Name != want || tool.From != from {
			t.Fatalf("%s: got %s from %q", id, tool.Name, tool.From)
		}
	}
	st := map[string]Status{}
	for _, s := range cfg.Statuses() {
		st[s.ID] = s
	}
	if s := st["flac"]; s.Active != "ffmpeg" || s.From != "AAC" || len(s.Tools) != 0 {
		t.Fatalf("flac status %+v", s)
	}
	if s := st["decoder"]; s.Active == "" {
		t.Fatalf("decoder status %+v", s)
	}
	// and it converts with the borrowed program
	src := filepath.Join(dir, "in.wav")
	tone(t, src, "-c:a", "pcm_s16le")
	info, _ := media.Inspect(src)
	out := filepath.Join(dir, "out.flac")
	if err := cfg.Run(context.Background(), Job{Src: src, Info: info, Format: "flac", Out: out, WorkDir: dir}); err != nil {
		t.Fatal(err)
	}
	if si, err := media.Inspect(out); err != nil || si.Format != "flac" {
		t.Fatalf("flac via borrowed ffmpeg: %+v %v", si, err)
	}
	// qaac covers Apple Lossless too (only checked for lookup: it's Windows-only)
	dir2 := t.TempDir()
	cfg2 := Config{Main: dir2}
	cfg2.MakeFolders()
	os.WriteFile(filepath.Join(dir2, "Encoders", "AAC", "qaac64"), []byte("#!/bin/sh\n"), 0o755)
	if tool, err := cfg2.Encoder("alac"); err != nil || tool.Kind != KindQaac || tool.From != "AAC" {
		t.Fatalf("qaac for alac: %+v %v", tool, err)
	}
	if _, err := cfg2.Encoder("mp3"); err == nil {
		t.Fatal("qaac can't write MP3")
	}
}
