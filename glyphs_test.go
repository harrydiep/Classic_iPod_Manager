package main

import (
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestThumbGlyphs(t *testing.T) {
	white := color.NRGBA{255, 255, 255, 255}
	for _, k := range []string{"play", "pause", "prev", "next"} {
		for _, size := range []int{16, 20, 24, 32} {
			img := thumbGlyph(k, size, white)
			var on, partial int
			for i := 3; i < len(img.Pix); i += 4 {
				px := img.Pix[i]
				switch {
				case px == 255:
					on++
				case px > 0:
					partial++
				}
			}
			// something drawn, with soft edges, and not filling the square
			if on == 0 || partial == 0 || on+partial > size*size*3/4 {
				t.Fatalf("%s at %d: %d solid, %d edge pixels", k, size, on, partial)
			}
			if dir := os.Getenv("GLYPH_DIR"); dir != "" {
				f, _ := os.Create(filepath.Join(dir, k+"-"+string(rune('0'+size/10))+string(rune('0'+size%10))+".png"))
				png.Encode(f, img)
				f.Close()
			}
		}
	}
	if thumbGlyph("nothing", 16, white).Pix[3] != 0 {
		t.Fatal("unknown glyph drew something")
	}
}
