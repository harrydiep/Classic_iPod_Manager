package ipod

import (
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

// The iPod keeps album art that iTunes copied over in
// iPod_Control/Artwork/ArtworkDB, with the pixels in .ithmb files
// (RGB565, little endian). This reads it so songs whose files have no
// embedded picture can still show their cover.

type thumbRef struct {
	format       uint32
	file         string
	offset, size uint32
	width        int
	height       int
	padX, padY   int
}

// Known thumbnail formats: id → stored width×height.
var thumbFormats = map[uint32][2]int{
	1028: {100, 100}, 1029: {200, 200}, // iPod Video (5G)
	1055: {128, 128}, 1060: {320, 320}, 1061: {56, 56}, // iPod classic / nano
	1056: {128, 128}, 1068: {128, 128}, 1071: {240, 240}, 1074: {50, 50}, 1078: {80, 80}, 1084: {240, 240},
	1073: {240, 240},                 // nano 5G
	1016: {140, 140}, 1017: {56, 56}, // iPod photo
	1027: {100, 100}, 1031: {42, 42}, // nano 1G/2G
}

func (d *Device) artworkIndex() map[uint64][]thumbRef {
	d.artOnce.Do(func() {
		d.art = map[uint64][]thumbRef{}
		data, err := os.ReadFile(filepath.Join(d.Root, "iPod_Control", "Artwork", "ArtworkDB"))
		if err != nil {
			return
		}
		parseArtworkDB(data, d.art)
	})
	return d.art
}

func parseArtworkDB(b []byte, out map[uint64][]thumbRef) {
	le := binary.LittleEndian
	if len(b) < 24 || string(b[:4]) != "mhfd" {
		return
	}
	off := int(le.Uint32(b[4:]))
	for off+16 <= len(b) && string(b[off:off+4]) == "mhsd" {
		hl := int(le.Uint32(b[off+4:]))
		tl := int(le.Uint32(b[off+8:]))
		typ := le.Uint16(b[off+12:])
		if tl < hl || off+tl > len(b) {
			return
		}
		if typ == 1 {
			parseMhli(b[off+hl:off+tl], out)
		}
		off += tl
	}
}

func parseMhli(b []byte, out map[uint64][]thumbRef) {
	le := binary.LittleEndian
	if len(b) < 12 || string(b[:4]) != "mhli" {
		return
	}
	n := int(le.Uint32(b[8:]))
	off := int(le.Uint32(b[4:]))
	for i := 0; i < n && off+36 <= len(b); i++ {
		if string(b[off:off+4]) != "mhii" {
			return
		}
		hl := int(le.Uint32(b[off+4:]))
		tl := int(le.Uint32(b[off+8:]))
		if tl < hl || off+tl > len(b) {
			return
		}
		song := le.Uint64(b[off+20:])
		// children: mhod type 2 → mhni → mhod type 3 (file name)
		for c := off + hl; c+24 <= off+tl; {
			if string(b[c:c+4]) != "mhod" {
				break
			}
			chl := int(le.Uint32(b[c+4:]))
			ctl := int(le.Uint32(b[c+8:]))
			if ctl < chl || c+ctl > off+tl {
				break
			}
			if le.Uint16(b[c+12:]) == 2 && c+chl+36 <= c+ctl && string(b[c+chl:c+chl+4]) == "mhni" {
				if r, ok := parseMhni(b[c+chl : c+ctl]); ok {
					out[song] = append(out[song], r)
				}
			}
			c += ctl
		}
		off += tl
	}
}

func parseMhni(b []byte) (thumbRef, bool) {
	le := binary.LittleEndian
	hl := int(le.Uint32(b[4:]))
	r := thumbRef{
		format: le.Uint32(b[16:]),
		offset: le.Uint32(b[20:]),
		size:   le.Uint32(b[24:]),
		padY:   int(int16(le.Uint16(b[28:]))),
		padX:   int(int16(le.Uint16(b[30:]))),
		height: int(int16(le.Uint16(b[32:]))),
		width:  int(int16(le.Uint16(b[34:]))),
	}
	// file name string mhod
	if hl+40 <= len(b) && string(b[hl:hl+4]) == "mhod" {
		m := b[hl:]
		n := int(le.Uint32(m[24:]))
		enc := m[28]
		if 36+n <= len(m) {
			s := m[36 : 36+n]
			if enc == 2 {
				u := make([]uint16, n/2)
				for i := range u {
					u[i] = le.Uint16(s[2*i:])
				}
				r.file = string(utf16.Decode(u))
			} else {
				r.file = string(s)
			}
		}
	}
	return r, r.file != "" && r.size > 0
}

// DBArtwork returns the largest cover image stored on the iPod for the
// track with the given database ID.
func (d *Device) DBArtwork(dbid uint64) (image.Image, error) {
	refs := d.artworkIndex()[dbid]
	if len(refs) == 0 {
		return nil, nil
	}
	best := refs[0]
	for _, r := range refs[1:] {
		if r.size > best.size {
			best = r
		}
	}
	fullW, fullH := 0, 0
	if f, ok := thumbFormats[best.format]; ok && f[0]*f[1]*2 <= int(best.size) {
		fullW, fullH = f[0], f[1]
	} else {
		side := int(math.Sqrt(float64(best.size / 2)))
		if side*side*2 != int(best.size) {
			return nil, errors.New("unsupported artwork format")
		}
		fullW, fullH = side, side
	}
	name := strings.TrimLeft(strings.ReplaceAll(best.file, ":", string(filepath.Separator)), string(filepath.Separator))
	f, err := os.Open(filepath.Join(d.Root, "iPod_Control", "Artwork", name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	px := make([]byte, best.size)
	if _, err := f.ReadAt(px, int64(best.offset)); err != nil {
		return nil, err
	}
	// The stored buffer is fullW×fullH; the picture sits at the padding offset.
	w, h := best.width-abs(best.padX), best.height-abs(best.padY)
	x0, y0 := max(best.padX, 0), max(best.padY, 0)
	if w <= 0 || x0+w > fullW {
		w = fullW - x0
	}
	if h <= 0 || y0+h > fullH {
		h = fullH - y0
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := ((y+y0)*fullW + (x + x0)) * 2
			v := binary.LittleEndian.Uint16(px[i:])
			r := uint8(v>>11) & 0x1f
			g := uint8(v>>5) & 0x3f
			b := uint8(v) & 0x1f
			img.SetRGBA(x, y, color.RGBA{r<<3 | r>>2, g<<2 | g>>4, b<<3 | b>>2, 255})
		}
	}
	return img, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
