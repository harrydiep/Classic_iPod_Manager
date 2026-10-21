package app

import (
	"bytes"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	"image/png"

	"podmanager/internal/media"
)

// Artwork is a song's cover image, ready for display.
type Artwork struct {
	DataURL string `json:"dataUrl"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	Source  string `json:"source"` // "file" or "ipod"
}

const maxArtSide = 600

// Artwork returns the cover image for a song: the picture embedded in its
// file, or else the thumbnail the iPod keeps in its artwork database.
// Returns nil when there is none.
func (a *App) Artwork(id uint32) (*Artwork, error) {
	a.mu.Lock()
	d := a.dev
	a.mu.Unlock()
	if d == nil {
		return nil, errors.New("no iPod is open")
	}
	a.mu.Lock()
	t := d.DB.TrackByID(id)
	var path string
	var dbid uint64
	if t != nil {
		path = d.AbsPath(t)
		dbid = t.DBID()
	}
	a.mu.Unlock()
	if t == nil {
		return nil, errors.New("song not found")
	}
	if pic, err := media.ReadArtwork(path); err == nil && pic != nil {
		if art := encodeArt(pic.Data, pic.MIME); art != nil {
			art.Source = "file"
			return art, nil
		}
	}
	img, err := d.DBArtwork(dbid)
	if err != nil || img == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil
	}
	b := img.Bounds()
	return &Artwork{DataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
		Width: b.Dx(), Height: b.Dy(), Source: "ipod"}, nil
}

// encodeArt turns image bytes into a data URL, shrinking very large images
// so they travel quickly to the interface.
func encodeArt(data []byte, mime string) *Artwork {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	art := &Artwork{Width: cfg.Width, Height: cfg.Height}
	if (cfg.Width <= maxArtSide && cfg.Height <= maxArtSide && len(data) < 600<<10) || (mime != "image/jpeg" && mime != "image/png" && mime != "image/gif") {
		art.DataURL = "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
		return art
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	small := shrink(img, maxArtSide)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 88}); err != nil {
		return nil
	}
	art.DataURL = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
	return art
}

// shrink scales img down (box filter) so its longest side is at most max.
func shrink(img image.Image, max int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= max && h <= max {
		return img
	}
	nw, nh := max, h*max/w
	if h > w {
		nw, nh = w*max/h, max
	}
	if nw < 1 {
		nw = 1
	}
	if nh < 1 {
		nh = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		sy0, sy1 := y*h/nh, (y+1)*h/nh
		for x := 0; x < nw; x++ {
			sx0, sx1 := x*w/nw, (x+1)*w/nw
			var r, g, bl, n uint32
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					cr, cg, cb, _ := img.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
					r, g, bl, n = r+cr, g+cg, bl+cb, n+1
				}
			}
			if n == 0 {
				n = 1
			}
			out.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 255})
		}
	}
	return out
}
