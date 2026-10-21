package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeAudio renders a 3 second tone with ffmpeg in the given format.
func makeAudio(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	out := filepath.Join(dir, name)
	cmd := append([]string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=3:sample_rate=44100", "-ac", "2"}, args...)
	cmd = append(cmd, "-y", out)
	if b, err := exec.Command("ffmpeg", cmd...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	return out
}

func testJPEG(t *testing.T, w, h int, c color.RGBA) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	jpeg.Encode(&b, img, &jpeg.Options{Quality: 90})
	return b.Bytes()
}

// ffprobeTags returns ffprobe's view of the format tags (lower-cased keys)
// and whether it sees an attached picture.
func probeTagsPic(t *testing.T, path string) (map[string]string, bool) {
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags:stream_tags:stream=codec_type,codec_name:stream_disposition=attached_pic", "-of", "flat", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	tags := map[string]string{}
	pic := false
	for _, line := range strings.Split(string(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		if strings.HasPrefix(k, "format.tags.") {
			tags[strings.ToLower(strings.TrimPrefix(k, "format.tags."))] = v
		}
		// Ogg tags belong to the audio stream
		if strings.HasPrefix(k, "streams.stream.0.tags.") {
			tags[strings.ToLower(strings.TrimPrefix(k, "streams.stream.0.tags."))] = v
		}
		if strings.HasSuffix(k, "disposition.attached_pic") && v == "1" {
			pic = true
		}
	}
	return tags, pic
}

func TestInspectFormats(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name, format string
		lossless     bool
		bits         int
		playable     bool
		args         []string
	}{
		{"a.flac", "flac", true, 24, false, []string{"-c:a", "flac", "-sample_fmt", "s32", "-metadata", "title=Flac Song", "-metadata", "artist=Ünïcode"}},
		{"a.ogg", "vorbis", false, 0, false, []string{"-c:a", "libvorbis", "-q:a", "4", "-metadata", "title=Vorbis Song", "-metadata", "artist=Someone"}},
		{"a.opus", "opus", false, 0, false, []string{"-c:a", "libopus", "-b:a", "96k"}},
		{"a.wma", "wma", false, 0, false, []string{"-c:a", "wmav2", "-b:a", "128k"}},
		{"a.mp3", "mp3", false, 0, true, []string{"-c:a", "libmp3lame", "-b:a", "192k"}},
		{"a.m4a", "alac", true, 16, true, []string{"-c:a", "alac", "-sample_fmt", "s16p"}},
		{"b.m4a", "aac", false, 0, true, []string{"-c:a", "aac", "-b:a", "160k"}},
		{"a.wav", "wav", true, 16, true, []string{"-c:a", "pcm_s16le"}},
		{"a.aiff", "aiff", true, 16, true, []string{"-c:a", "pcm_s16be"}},
	}
	for _, c := range cases {
		p := makeAudio(t, dir, c.name, c.args...)
		si, err := Inspect(p)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if si.Format != c.format || si.Lossless != c.lossless || si.Playable != c.playable || si.Bits != c.bits {
			t.Errorf("%s: %+v", c.name, si)
		}
		if c.format != "wma" && (si.LengthMs < 2900 || si.LengthMs > 3100) {
			t.Errorf("%s: length %d", c.name, si.LengthMs)
		}
		if c.format != "wma" && (si.SampleRate != 44100 && !(c.format == "opus" && si.SampleRate == 48000)) {
			t.Errorf("%s: rate %d", c.name, si.SampleRate)
		}
		if c.format != "wma" && si.Channels != 2 {
			t.Errorf("%s: channels %d", c.name, si.Channels)
		}
		if si.Label == "" {
			t.Errorf("%s: no label", c.name)
		}
	}
	si, _ := Inspect(filepath.Join(dir, "a.flac"))
	if si.Tags.Title != "Flac Song" || si.Tags.Artist != "Ünïcode" {
		t.Errorf("flac tags %+v", si.Tags)
	}
	si, _ = Inspect(filepath.Join(dir, "a.ogg"))
	if si.Tags.Title != "Vorbis Song" || si.Bitrate == 0 || si.Tags.Composer != "" {
		t.Errorf("ogg %+v", si)
	}
}

func TestWriteTagsArt(t *testing.T) {
	dir := t.TempDir()
	red := testJPEG(t, 300, 300, color.RGBA{200, 20, 20, 255})
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, image.NewRGBA(image.Rect(0, 0, 64, 64)))
	files := []string{
		makeAudio(t, dir, "s.mp3", "-c:a", "libmp3lame", "-b:a", "128k"),
		makeAudio(t, dir, "s.m4a", "-c:a", "aac", "-b:a", "128k"),
		makeAudio(t, dir, "l.m4a", "-c:a", "alac"),
		makeAudio(t, dir, "s.flac", "-c:a", "flac", "-metadata", "REPLAYGAIN_TRACK_GAIN=-3 dB"),
		makeAudio(t, dir, "s.ogg", "-c:a", "libvorbis", "-metadata", "REPLAYGAIN_TRACK_GAIN=-2 dB"),
		makeAudio(t, dir, "s.opus", "-c:a", "libopus"),
	}
	tags := Tags{Title: "Ärt Test", Artist: "Painter", Album: "Gallery", AlbumArtist: "Various", Genre: "Ambient",
		Year: 2004, Track: 3, TrackTotal: 9, Disc: 1, DiscTotal: 2, Comment: "hello", Composer: "C", BPM: 120, Compilation: true}
	for _, p := range files {
		if err := WriteTagsArt(p, tags, ArtEdit{Set: &Picture{Data: red}}); err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		pic, err := ReadArtwork(p)
		if err != nil || pic == nil || !bytes.Equal(pic.Data, red) || pic.MIME != "image/jpeg" {
			t.Fatalf("%s: art not read back (%v)", p, err)
		}
		ft, hasPic := probeTagsPic(t, p)
		if ft["title"] != "Ärt Test" || ft["artist"] != "Painter" || ft["album"] != "Gallery" || !hasPic {
			t.Fatalf("%s: ffprobe sees %v pic=%v", p, ft, hasPic)
		}
		si, err := Inspect(p)
		if err != nil || si.Tags.Title != "Ärt Test" || si.Tags.Track != 3 || si.Tags.Year != 2004 || !si.HasArt {
			t.Fatalf("%s: inspect %+v %v", p, si, err)
		}
		if si.LengthMs < 2900 {
			t.Fatalf("%s: audio damaged, length %d", p, si.LengthMs)
		}
		// replace with a PNG, then remove
		if err := WriteTagsArt(p, tags, ArtEdit{Set: &Picture{Data: pngBuf.Bytes()}}); err != nil {
			t.Fatal(err)
		}
		if pic, _ := ReadArtwork(p); pic == nil || pic.MIME != "image/png" {
			t.Fatalf("%s: png art", p)
		}
		if err := WriteTagsArt(p, tags, ArtEdit{Remove: true}); err != nil {
			t.Fatal(err)
		}
		if pic, _ := ReadArtwork(p); pic != nil {
			t.Fatalf("%s: art not removed", p)
		}
		// tags-only write keeps whatever art there is
		WriteTagsArt(p, tags, ArtEdit{Set: &Picture{Data: red}})
		tags2 := tags
		tags2.Title = "Second"
		if err := WriteTags(p, tags2); err != nil {
			t.Fatal(err)
		}
		if pic, _ := ReadArtwork(p); pic == nil {
			t.Fatalf("%s: tag-only write dropped art", p)
		}
		// Ogg pages and checksums are valid
		if strings.HasSuffix(p, ".ogg") || strings.HasSuffix(p, ".opus") {
			if _, err := exec.LookPath("ogginfo"); err == nil {
				out, err := exec.Command("ogginfo", p).CombinedOutput()
				if err != nil || bytes.Contains(out, []byte("WARNING")) || bytes.Contains(out, []byte("ERROR")) {
					t.Fatalf("%s: ogginfo: %v\n%s", p, err, out)
				}
			}
		}
		// decodes cleanly after all the edits
		if b, err := exec.Command("ffmpeg", "-v", "error", "-i", p, "-f", "null", "-").CombinedOutput(); err != nil || len(bytes.TrimSpace(b)) > 0 {
			t.Fatalf("%s: decode after edits: %v %s", p, err, b)
		}
	}
	// FLAC keeps fields it doesn't manage
	ft, _ := probeTagsPic(t, files[3])
	if ft["replaygain_track_gain"] != "-3 dB" || ft["title"] != "Second" {
		t.Fatalf("flac kept fields: %v", ft)
	}
	if ft, _ := probeTagsPic(t, files[4]); ft["replaygain_track_gain"] != "-2 dB" || ft["title"] != "Second" {
		t.Fatalf("ogg kept fields: %v", ft)
	}
	// large FLAC tag growth forces a rewrite; the file must stay valid
	big := testJPEG(t, 1400, 1400, color.RGBA{1, 2, 3, 255})
	if err := WriteTagsArt(files[3], tags, ArtEdit{Set: &Picture{Data: append(big, make([]byte, 200000)...)}}); err != nil {
		t.Fatal(err)
	}
	if si, err := Inspect(files[3]); err != nil || si.LengthMs < 2900 {
		t.Fatalf("flac after growth: %v", err)
	}
	if _, err := os.Stat(files[3]); err != nil {
		t.Fatal(err)
	}
}
