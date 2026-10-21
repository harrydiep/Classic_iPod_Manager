package media

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Tests run against sample files in $MEDIA_TEST_DIR (generated with ffmpeg);
// they are skipped when it isn't set.
func dir(t *testing.T) string {
	d := os.Getenv("MEDIA_TEST_DIR")
	if d == "" {
		t.Skip("MEDIA_TEST_DIR not set")
	}
	return d
}

func copyFile(t *testing.T, src string) string {
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	out, _ := os.Create(dst)
	io.Copy(out, in)
	out.Close()
	return dst
}

func TestProbe(t *testing.T) {
	d := dir(t)
	cases := map[string]string{"cbr.mp3": "mp3", "vbr.mp3": "mp3", "mono24.mp3": "mp3", "aac.m4a": "aac",
		"fast.m4a": "aac", "alac.m4a": "alac", "wav.wav": "wav", "aiff.aiff": "aiff", "art.mp3": "mp3"}
	for name, format := range cases {
		info, err := Probe(filepath.Join(d, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if info.Format != format || info.LengthMs < 7300 || info.LengthMs > 7700 {
			t.Errorf("%s: format %s length %d", name, info.Format, info.LengthMs)
		}
		t.Logf("%s: %s %dms %dkbps %dHz vbr=%v title=%q artist=%q track=%d/%d", name, info.Format, info.LengthMs,
			info.Bitrate, info.SampleRate, info.VBR, info.Tags.Title, info.Tags.Artist, info.Tags.Track, info.Tags.TrackTotal)
	}
	if _, err := Probe(filepath.Join(d, "x.flac")); err == nil {
		t.Error("flac should be rejected")
	}
	info, _ := Probe(filepath.Join(d, "cbr.mp3"))
	if info.Tags.Title != "Test Song" || info.Tags.Artist != "Tést Ärtist" || info.Tags.Track != 3 || info.Tags.TrackTotal != 12 || info.Tags.Year != 2004 {
		t.Errorf("mp3 tags: %+v", info.Tags)
	}
	info, _ = Probe(filepath.Join(d, "aac.m4a"))
	if info.Tags.Title != "AAC Song" || info.Tags.Track != 5 || info.Tags.Disc != 1 || info.Tags.DiscTotal != 2 {
		t.Errorf("m4a tags: %+v", info.Tags)
	}
}

var newTags = Tags{Title: "Nouveau Tïtle ☃", Artist: "Somebody", Album: "Fresh Album", AlbumArtist: "Various Artists",
	Genre: "Jazz", Composer: "J. S. Bach", Comment: "edited by PodManager", Grouping: "Grp", Year: 1987,
	Track: 7, TrackTotal: 14, Disc: 2, DiscTotal: 3, BPM: 120, Compilation: true}

func ffprobeTags(t *testing.T, path string) (string, string) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		return "", ""
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags:stream=codec_name,codec_type", "-of", "default=nw=1", path).CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe %s: %v %s", path, err, out)
	}
	dec, err := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a", "-f", "null", "-").CombinedOutput()
	if err != nil || len(strings.TrimSpace(string(dec))) > 0 {
		t.Errorf("decode errors for %s: %v %s", path, err, dec)
	}
	return string(out), string(dec)
}

func TestWriteTags(t *testing.T) {
	d := dir(t)
	for _, name := range []string{"cbr.mp3", "vbr.mp3", "mono24.mp3", "art.mp3", "aac.m4a", "fast.m4a", "alac.m4a", "art.m4a"} {
		p := copyFile(t, filepath.Join(d, name))
		before, _ := Probe(p)
		if err := WriteTags(p, newTags); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		// write twice (exercises the in-place / free-atom paths)
		tags2 := newTags
		tags2.Comment = "second pass, a bit longer than before"
		if err := WriteTags(p, tags2); err != nil {
			t.Fatalf("%s second: %v", name, err)
		}
		after, err := Probe(p)
		if err != nil {
			t.Fatalf("%s reprobe: %v", name, err)
		}
		got := after.Tags
		want := tags2
		if got.Title != want.Title || got.Artist != want.Artist || got.Album != want.Album || got.AlbumArtist != want.AlbumArtist ||
			got.Genre != want.Genre || got.Composer != want.Composer || got.Year != want.Year || got.Track != want.Track ||
			got.TrackTotal != want.TrackTotal || got.Disc != want.Disc || got.DiscTotal != want.DiscTotal ||
			got.Comment != want.Comment || got.BPM != want.BPM || !got.Compilation || got.Grouping != want.Grouping {
			t.Errorf("%s: tags mismatch\n got  %+v\n want %+v", name, got, want)
		}
		if after.LengthMs != before.LengthMs {
			t.Errorf("%s: length changed %d -> %d", name, before.LengthMs, after.LengthMs)
		}
		meta, _ := ffprobeTags(t, p)
		if strings.HasPrefix(name, "art") && !strings.Contains(meta, "codec_name=png") {
			t.Errorf("%s: artwork lost:\n%s", name, meta)
		}
		t.Logf("%s ok (%d -> %d bytes)", name, before.Size, after.Size)
	}
}

func TestReadArtwork(t *testing.T) {
	d := dir(t)
	for _, name := range []string{"art.mp3", "art.m4a"} {
		p := copyFile(t, filepath.Join(d, name))
		// must survive a tag rewrite too
		if err := WriteTags(p, newTags); err != nil {
			t.Fatal(err)
		}
		pic, err := ReadArtwork(p)
		if err != nil || pic == nil || pic.MIME != "image/png" || len(pic.Data) < 50 {
			t.Fatalf("%s: %v %+v", name, err, pic)
		}
	}
	for _, name := range []string{"cbr.mp3", "aac.m4a", "wav.wav"} {
		if pic, _ := ReadArtwork(filepath.Join(d, name)); pic != nil {
			t.Fatalf("%s: unexpected artwork", name)
		}
	}
}
