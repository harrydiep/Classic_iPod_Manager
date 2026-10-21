package ipod

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// IPOD_ART_ROOT points at an iPod folder whose ArtworkDB was written by
// libgpod (first two tracks have a 300x300 red→blue gradient cover).
func TestDBArtwork(t *testing.T) {
	root := os.Getenv("IPOD_ART_ROOT")
	if root == "" {
		t.Skip("IPOD_ART_ROOT not set")
	}
	d, err := Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for i, tr := range d.DB.Tracks {
		img, err := d.DBArtwork(tr.DBID())
		if err != nil {
			t.Fatal(err)
		}
		if img == nil {
			if i < 2 {
				t.Fatalf("track %d has no artwork", i)
			}
			continue
		}
		found++
		b := img.Bounds()
		if b.Dx() != 200 || b.Dy() != 200 {
			t.Fatalf("size %v", b)
		}
		r, _, bl, _ := img.At(2, 2).RGBA()
		r2, _, bl2, _ := img.At(197, 197).RGBA()
		t.Logf("corner colours: (%d,%d) (%d,%d)", r>>8, bl>>8, r2>>8, bl2>>8)
		if out := os.Getenv("ART_OUT"); out != "" {
			f, _ := os.Create(filepath.Join(out, tr.Title()+".png"))
			png.Encode(f, img)
			f.Close()
		}
	}
	if found != 2 {
		t.Fatalf("found %d covers", found)
	}
}
