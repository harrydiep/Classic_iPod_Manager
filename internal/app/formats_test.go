package app

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"podmanager/internal/convert"
	"podmanager/internal/ipod"
	"podmanager/internal/media"
)

func fmTone(t *testing.T, path string, args ...string) {
	t.Helper()
	a := append([]string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=500:duration=4:sample_rate=" + fmRate(args), "-ac", "2"}, fmArgs(args)...)
	if b, err := exec.Command("ffmpeg", append(a, "-y", path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
}
func fmRate(a []string) string {
	if len(a) > 0 && strings.HasPrefix(a[0], "rate=") {
		return a[0][5:]
	}
	return "44100"
}
func fmArgs(a []string) []string {
	if len(a) > 0 && strings.HasPrefix(a[0], "rate=") {
		return a[1:]
	}
	return a
}

func colourJPEG(c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 240, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 240; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

func avg(img image.Image) (int, int, int) {
	var r, g, b, n int
	bd := img.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r, g, b, n = r+int(cr>>8), g+int(cg>>8), b+int(cb>>8), n+1
		}
	}
	return r / n, g / n, b / n
}

func linkTool(t *testing.T, dir, name string) {
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s not installed", name)
	}
	os.MkdirAll(dir, 0o755)
	os.Symlink(p, filepath.Join(dir, name))
}

func TestFormatManager(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	base := t.TempDir()
	root := filepath.Join(base, "ipod")
	music := filepath.Join(base, "music")
	main := filepath.Join(base, "PodManager")
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	os.MkdirAll(music, 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("ModelNumStr: xA147\n"), 0o644)

	// sources
	mp3 := filepath.Join(music, "one.mp3")
	fmTone(t, mp3, "-c:a", "libmp3lame", "-b:a", "192k")
	media.WriteTagsArt(mp3, media.Tags{Title: "Song One", Artist: "Band", Album: "First", Track: 1},
		media.ArtEdit{Set: &media.Picture{Data: colourJPEG(color.RGBA{230, 20, 20, 255})}})
	flac := filepath.Join(music, "Twö.flac")
	fmTone(t, flac, "rate=96000", "-c:a", "flac", "-sample_fmt", "s32")
	media.WriteTagsArt(flac, media.Tags{Title: "Song Two", Artist: "Band", Album: "Second", Track: 3, TrackTotal: 9}, media.ArtEdit{})
	wav := filepath.Join(music, "three.wav")
	fmTone(t, wav, "-c:a", "pcm_s16le")
	green := filepath.Join(music, "green.jpg")
	os.WriteFile(green, colourJPEG(color.RGBA{20, 200, 30, 255}), 0o644)
	blue := filepath.Join(music, "blue.jpg")
	os.WriteFile(blue, colourJPEG(color.RGBA{20, 40, 220, 255}), 0o644)

	plat := &fakePlat{root: root, pick: music}
	ev := make(chan Event, 1000)
	a := New(plat, t.TempDir(), func(e Event) { ev <- e })
	a.SetPrefsFile(filepath.Join(base, "settings.json"))
	a.SetMainFolder(main)
	for _, d := range []string{"Encoders/MP3", "Encoders/AAC", "Encoders/Apple Lossless", "Encoders/FLAC", "Encoders/Ogg Vorbis", "Encoders/Decoder", "Converted Music", "iPod Originals"} {
		if _, err := os.Stat(filepath.Join(main, d)); err != nil {
			t.Fatalf("main folder layout: %v", err)
		}
	}
	linkTool(t, filepath.Join(main, "Encoders", "AAC"), "ffmpeg")
	linkTool(t, filepath.Join(main, "Encoders", "MP3"), "lame")

	info, _ := a.Encoders()
	st := map[string]convert.Status{}
	for _, s := range info.Encoders {
		st[s.ID] = s
	}
	if !info.Writable || st["aac"].Active != "ffmpeg" || st["mp3"].Active != "lame" || st["flac"].Active != "ffmpeg" || st["flac"].From != "AAC" || !strings.HasPrefix(st["decoder"].Active, "ffmpeg") {
		t.Fatalf("encoders: %+v", info)
	}
	// a missing encoder is reported before anything starts: lame only
	// writes MP3, so with the FFmpeg folder moved elsewhere FLAC has none
	a.SetEncoder("aac", filepath.Join(base, "nothing-here"), "")
	var ne convert.ErrNoEncoder
	if err := a.FMRun(FMRequest{Items: []FMEdit{{Key: "file:" + wav, Source: "file", Path: wav}}, Convert: true, Format: "flac"}); !errors.As(err, &ne) {
		t.Fatalf("want ErrNoEncoder, got %v", err)
	}
	a.SetEncoder("aac", "", "")

	// put the MP3 on the iPod: its cover goes into the iPod's artwork database
	if _, err := a.Initialize(root, "Test iPod"); err != nil {
		t.Fatal(err)
	}
	a.AddPaths([]string{mp3})
	if r := waitDone(t, ev); r.OK != 1 {
		t.Fatalf("add: %+v", r)
	}
	lib, _ := a.Library()
	id := lib.Tracks[0].ID
	a.mu.Lock()
	tr := a.dev.DB.TrackByID(id)
	tr.SetPlayCount(7)
	oldPath := a.dev.AbsPath(tr)
	img, _ := a.dev.DBArtwork(tr.DBID())
	a.mu.Unlock()
	if img == nil {
		t.Fatal("added song has no cover on the iPod")
	}
	if r, _, _ := avg(img); r < 200 {
		t.Fatalf("cover colour %v", r)
	}

	items, err := a.FMFromIPod([]uint32{id})
	if err != nil || len(items) != 1 || items[0].Info == nil || items[0].Info.Format != "mp3" || !items[0].Info.HasArt || items[0].Info.Tags.Title != "Song One" {
		t.Fatalf("from iPod: %+v %v", items, err)
	}
	files, _ := a.FMInspect([]string{music})
	if len(files) != 3 {
		t.Fatalf("inspect folder: %d items", len(files))
	}

	// convert: the iPod song to AAC in place, the FLAC to AAC and onto the iPod
	newTitle := "Song One (AAC)"
	newArtist := "Bänd"
	err = a.FMRun(FMRequest{Convert: true, Format: "aac", Quality: "cbr192", AddToIPod: true, Items: []FMEdit{
		{Key: items[0].Key, Source: "ipod", TrackID: id, Patch: Patch{Title: &newTitle}},
		{Key: "file:" + flac, Source: "file", Path: flac, Patch: Patch{Artist: &newArtist}, Art: "set", ArtPath: green},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r := waitDone(t, ev)
	if r.Error != "" || r.OK != 2 || len(r.Errors) != 0 || len(r.FM) != 2 {
		t.Fatalf("convert: %+v", r)
	}
	// the iPod original was backed up first
	backup := filepath.Join(main, "iPod Originals", "Band", "First", "01 Song One.mp3")
	if st, err := os.Stat(backup); err != nil || st.Size() == 0 {
		t.Fatalf("backup: %v", err)
	}
	// converted copies
	c1 := filepath.Join(main, "Converted Music", "Band", "First", "01 Song One (AAC).m4a")
	c2 := filepath.Join(main, "Converted Music", "Bänd", "Second", "03 Song Two.m4a")
	for _, p := range []string{c1, c2} {
		si, err := media.Inspect(p)
		if err != nil || si.Format != "aac" || si.SampleRate > 48000 {
			t.Fatalf("converted copy %s: %+v %v", p, si, err)
		}
	}
	if si, _ := media.Inspect(c2); si.Tags.Artist != "Bänd" || !si.HasArt || si.Tags.Track != 3 {
		t.Fatalf("flac copy tags: %+v", si)
	}
	// the iPod song now uses the AAC file and kept its history
	lib, _ = a.Library()
	if len(lib.Tracks) != 2 {
		t.Fatalf("tracks on iPod: %d", len(lib.Tracks))
	}
	var one, two TrackView
	for _, x := range lib.Tracks {
		if x.ID == id {
			one = x
		} else {
			two = x
		}
	}
	if one.Title != newTitle || one.Kind != "AAC audio file" || one.Plays != 7 || !strings.HasSuffix(one.Path, ".m4a") {
		t.Fatalf("replaced track: %+v", one)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old file still on the iPod: %v", err)
	}
	if two.Title != "Song Two" || two.Artist != "Bänd" || two.SampleRate != 48000 {
		t.Fatalf("added track: %+v", two)
	}
	// covers on the iPod: song one kept red, song two got green
	d, err := ipod.Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range d.DB.Tracks {
		img, _ := d.DBArtwork(x.DBID())
		if img == nil {
			t.Fatalf("%s has no cover on the iPod", x.Title())
		}
		r, g, _ := avg(img)
		if (x.ID() == id && r < 200) || (x.ID() != id && g < 180) {
			t.Fatalf("%s cover colour %d,%d", x.Title(), r, g)
		}
	}

	// save info: new cover for the iPod song, cover removed from a file
	genre := "Test Genre"
	err = a.FMRun(FMRequest{Items: []FMEdit{
		{Key: items[0].Key, Source: "ipod", TrackID: id, Patch: Patch{Genre: &genre}, Art: "set", ArtPath: blue},
		{Key: "file:" + c2, Source: "file", Path: c2, Patch: Patch{Genre: &genre}, Art: "remove"},
		{Key: "file:" + wav, Source: "file", Path: wav, Patch: Patch{Genre: &genre}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	r = waitDone(t, ev)
	if r.OK != 2 || len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "can't store") {
		t.Fatalf("retag: %+v", r)
	}
	if si, _ := media.Inspect(c2); si.Tags.Genre != genre || si.HasArt {
		t.Fatalf("retagged file: %+v", si)
	}
	art, _ := a.Artwork(id)
	if art == nil || art.Source != "file" {
		t.Fatalf("iPod song file art: %+v", art)
	}
	d, _ = ipod.Open(root, "")
	tr2 := d.DB.TrackByID(id)
	img, _ = d.DBArtwork(tr2.DBID())
	if _, _, b := avg(img); b < 180 || tr2.Genre() != genre {
		t.Fatalf("iPod cover not blue (%d) or genre %q", b, tr2.Genre())
	}

	// libgpod reads the result
	if tool := os.Getenv("LIBGPOD_ARTCHECK"); tool != "" {
		out, err := exec.Command(tool, root).CombinedOutput()
		if err != nil || strings.Count(string(out), "thumb=200x200") != 2 || strings.Contains(string(out), "ERROR") {
			t.Fatalf("libgpod: %v\n%s", err, out)
		}
		t.Logf("libgpod:\n%s", out)
	}
	// folders open in Explorer
	if err := a.OpenFolder("converted"); err != nil || plat.opened != filepath.Join(main, "Converted Music") {
		t.Fatalf("open folder: %v %s", err, plat.opened)
	}
	if err := a.OpenFolder("flac"); err != nil || plat.opened != filepath.Join(main, "Encoders", "FLAC") {
		t.Fatalf("open encoder folder: %v %s", err, plat.opened)
	}
	// custom folder and program choice
	info, _ = a.SetEncoder("aac", filepath.Join(base, "elsewhere"), "")
	for _, s := range info.Encoders {
		if s.ID == "aac" && (s.Path != filepath.Join(base, "elsewhere") || s.Active != "" || !s.Exists) {
			t.Fatalf("custom folder: %+v", s)
		}
	}
	// picture preview
	if p, err := a.FMImagePreview(green); err != nil || p.Art == nil || !strings.HasPrefix(p.Art.DataURL, "data:image/jpeg") {
		t.Fatalf("preview: %v", err)
	}
	if _, err := a.FMImagePreview(wav); err == nil {
		t.Fatal("a WAV isn't a picture")
	}
	// no temp files left in the main folder
	if left, _ := os.ReadDir(filepath.Join(main, "Temp")); len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

func TestFormatManagerStrip(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	base := t.TempDir()
	music := filepath.Join(base, "music")
	main := filepath.Join(base, "Main")
	os.MkdirAll(music, 0o755)
	src := filepath.Join(music, "one.mp3")
	fmTone(t, src, "-c:a", "libmp3lame", "-b:a", "192k")
	media.WriteTagsArt(src, media.Tags{Title: "Song One", Artist: "Band", Album: "First", Track: 4, TrackTotal: 10,
		Comment: "secret note", Genre: "Rock", Year: 2001},
		media.ArtEdit{Set: &media.Picture{Data: colourJPEG(color.RGBA{230, 20, 20, 255})}})
	ev := make(chan Event, 100)
	a := New(&fakePlat{pick: music}, t.TempDir(), func(e Event) { ev <- e })
	a.SetPrefsFile(filepath.Join(base, "settings.json"))
	a.SetMainFolder(main)
	linkTool(t, filepath.Join(main, "Encoders", "AAC"), "ffmpeg")
	err := a.FMRun(FMRequest{Convert: true, Format: "aac", Strip: []string{"comment", "album", "track", "artwork"},
		Items: []FMEdit{{Key: "file:" + src, Source: "file", Path: src}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := waitDone(t, ev); r.OK != 1 || len(r.Errors) != 0 {
		t.Fatalf("convert: %+v", r)
	}
	// still filed by album and track number, but without them inside
	out := filepath.Join(main, "Converted Music", "Band", "First", "04 Song One.m4a")
	si, err := media.Inspect(out)
	if err != nil {
		t.Fatal(err)
	}
	tg := si.Tags
	if si.HasArt || tg.Comment != "" || tg.Album != "" || tg.Track != 0 || tg.TrackTotal != 0 ||
		tg.Title != "Song One" || tg.Artist != "Band" || tg.Genre != "Rock" || tg.Year != 2001 {
		t.Fatalf("tags of the converted file: %+v art=%v", tg, si.HasArt)
	}
}
