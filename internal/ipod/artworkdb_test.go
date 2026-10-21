package ipod

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"podmanager/internal/itdb"
)

func solid(w, h int, c color.RGBA, asPNG bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if asPNG {
		png.Encode(&b, img)
	} else {
		jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	}
	return b.Bytes()
}

func newTestIPod(t *testing.T) *Device {
	t.Helper()
	root := t.TempDir()
	d, err := Initialize(root, "Art iPod", "")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("ModelNumStr: xA147\nvisibleBuildID: 0x01130000 (1.3)\n"), 0o644)
	d.Model, _ = ReadModel(root)
	for i, title := range []string{"Red Song", "Blue Song", "Green Song", "No Art"} {
		loc := fmt.Sprintf(":iPod_Control:Music:F0%d:T%d.mp3", i, i)
		os.WriteFile(filepath.Join(root, "iPod_Control", "Music", fmt.Sprintf("F0%d", i), fmt.Sprintf("T%d.mp3", i)), []byte("x"), 0o644)
		d.DB.NewTrack(itdb.NewTrackInfo{Location: loc, FileTypeCode: "MP3 ", Kind: "MPEG audio file", Size: 1, LengthMs: 1000,
			Bitrate: 128, SampleRate: 44100, Title: title, Artist: "A", Album: "B"})
	}
	return d
}

// libgpodCheck runs the libgpod-based checker (see artcheck.c) if present.
func libgpodCheck(t *testing.T, root string) map[string]string {
	tool := os.Getenv("LIBGPOD_ARTCHECK")
	if tool == "" {
		return nil
	}
	out, err := exec.Command(tool, root).CombinedOutput()
	if err != nil || strings.Contains(string(out), "ERROR") {
		t.Fatalf("libgpod: %v\n%s", err, out)
	}
	res := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimPrefix(line, "T ")
		if i := strings.Index(line, " has="); i > 0 {
			res[line[:i]] = line[i+1:]
		}
	}
	return res
}

func avgColor(img image.Image) (int, int, int) {
	var r, g, b, n int
	bd := img.Bounds()
	for y := bd.Min.Y; y < bd.Max.Y; y++ {
		for x := bd.Min.X; x < bd.Max.X; x++ {
			cr, cg, cb, _ := img.At(x, y).RGBA()
			r += int(cr >> 8)
			g += int(cg >> 8)
			b += int(cb >> 8)
			n++
		}
	}
	return r / n, g / n, b / n
}

func near(a, b int) bool { return a-b < 10 && b-a < 10 }

func TestWriteArtwork(t *testing.T) {
	d := newTestIPod(t)
	tr := d.DB.Tracks
	red := solid(300, 300, color.RGBA{220, 30, 30, 255}, false)
	// a wide picture with a blue middle: the centred square crop is all blue
	wide := image.NewRGBA(image.Rect(0, 0, 640, 320))
	for y := 0; y < 320; y++ {
		for x := 0; x < 640; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x >= 160 && x < 480 {
				c = color.RGBA{20, 40, 230, 255}
			}
			wide.Set(x, y, c)
		}
	}
	var wb bytes.Buffer
	png.Encode(&wb, wide)
	green := solid(64, 64, color.RGBA{30, 200, 40, 255}, true) // small: enlarged

	for i, pic := range [][]byte{red, wb.Bytes(), green} {
		if err := d.SetArtwork(tr[i], pic); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	want := map[string][3]int{"Red Song": {220, 30, 30}, "Blue Song": {20, 40, 230}, "Green Song": {30, 200, 40}}
	check := func(stage string) {
		// our own reader, from a fresh open
		d2, err := Open(d.Root, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, x := range d2.DB.Tracks {
			img, err := d2.DBArtwork(x.DBID())
			w, ok := want[x.Title()]
			if !ok {
				if img != nil || x.HasArtwork() {
					t.Fatalf("%s: %s should have no art", stage, x.Title())
				}
				continue
			}
			if err != nil || img == nil || img.Bounds().Dx() != 200 || !x.HasArtwork() {
				t.Fatalf("%s: %s art missing: %v", stage, x.Title(), err)
			}
			r, g, b := avgColor(img)
			if !near(r, w[0]) || !near(g, w[1]) || !near(b, w[2]) {
				t.Fatalf("%s: %s colour %d,%d,%d want %v", stage, x.Title(), r, g, b, w)
			}
		}
		// libgpod's view
		lg := libgpodCheck(t, d.Root)
		if lg == nil {
			return
		}
		for title, w := range want {
			line := lg[title]
			var has, count, size, link, tw, th, r, g, b int
			if _, err := fmt.Sscanf(line, "has=%d count=%d size=%d link=%d thumb=%dx%d avg=%d,%d,%d", &has, &count, &size, &link, &tw, &th, &r, &g, &b); err != nil {
				t.Fatalf("%s: libgpod line for %s: %q (%v)", stage, title, line, err)
			}
			if has != 1 || count != 1 || size == 0 || link < 100 || tw != 200 || th != 200 || !near(r, w[0]) || !near(g, w[1]) || !near(b, w[2]) {
				t.Fatalf("%s: libgpod sees %s: %s", stage, title, line)
			}
		}
		for title, line := range lg {
			if _, ok := want[title]; !ok && !strings.Contains(line, "thumb=none") {
				t.Fatalf("%s: libgpod sees art on %s: %s", stage, title, line)
			}
		}
	}
	check("first save")

	// replace one cover many times (forces compaction), remove another and
	// delete a song that had a cover
	for i := 0; i < 16; i++ {
		c := color.RGBA{uint8(i * 10), 100, 100, 255}
		if err := d.SetArtwork(tr[2], solid(200, 200, c, false)); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.SetArtwork(tr[2], green); err != nil {
		t.Fatal(err)
	}
	if err := d.RemoveArtwork(tr[0]); err != nil {
		t.Fatal(err)
	}
	delete(want, "Red Song")
	blueID := tr[1].ID()
	d.DB.RemoveTracks([]uint32{blueID})
	delete(want, "Blue Song")
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	check("after edits")
	entries, _ := os.ReadDir(d.artDir())
	var names []string
	var total int64
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".ithmb") {
			names = append(names, e.Name())
			info, _ := e.Info()
			total += info.Size()
		}
	}
	// one live cover: 80000 + 20000 bytes, after compaction
	if total != 100000 || len(names) != 2 {
		t.Fatalf("ithmb files not compacted: %v (%d bytes)", names, total)
	}
	// an unchanged save leaves the database alone
	st1, _ := os.Stat(d.artDBPath())
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	st2, _ := os.Stat(d.artDBPath())
	if !st1.ModTime().Equal(st2.ModTime()) {
		t.Fatal("ArtworkDB rewritten without changes")
	}
}

// A database written by libgpod survives a round trip through our writer
// with its covers intact.
func TestArtworkDBRoundTrip(t *testing.T) {
	root := os.Getenv("IPOD_ART_ROOT")
	if root == "" {
		t.Skip("IPOD_ART_ROOT not set")
	}
	b, err := os.ReadFile(filepath.Join(root, "iPod_Control", "Artwork", "ArtworkDB"))
	if err != nil {
		t.Fatal(err)
	}
	db, err := parseArtDBFull(b)
	if err != nil {
		t.Fatal(err)
	}
	out := db.bytes()
	if !bytes.Equal(out, b) {
		// find the first difference for the log
		i := 0
		for i < len(out) && i < len(b) && out[i] == b[i] {
			i++
		}
		t.Fatalf("round trip differs at %#x (len %d vs %d)", i, len(out), len(b))
	}
}
