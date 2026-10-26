package main

import (
	"image"
	"image/color"
)

// Media-control glyphs for the buttons under the taskbar preview (see
// thumbbar_windows.go). They're drawn here rather than shipped as icon files
// so they can be any size and follow Windows' light or dark theme.

type glyphPoint struct{ x, y float64 }

// glyph shapes on a 16×16 grid; each is a list of convex polygons
var glyphShapes = map[string][][]glyphPoint{
	"play":  {{{4.5, 2.5}, {13.5, 8}, {4.5, 13.5}}},
	"pause": {{{3.5, 2.5}, {6.8, 2.5}, {6.8, 13.5}, {3.5, 13.5}}, {{9.2, 2.5}, {12.5, 2.5}, {12.5, 13.5}, {9.2, 13.5}}},
	"prev":  {{{2.5, 3}, {4.6, 3}, {4.6, 13}, {2.5, 13}}, {{13.5, 3}, {13.5, 13}, {5, 8}}},
	"next":  {{{11.4, 3}, {13.5, 3}, {13.5, 13}, {11.4, 13}}, {{2.5, 3}, {11, 8}, {2.5, 13}}},
}

// inConvex reports whether p is inside the convex polygon (either winding).
func inConvex(poly []glyphPoint, p glyphPoint) bool {
	sign := 0
	for i := range poly {
		a, b := poly[i], poly[(i+1)%len(poly)]
		cross := (b.x-a.x)*(p.y-a.y) - (b.y-a.y)*(p.x-a.x)
		s := 0
		if cross > 0 {
			s = 1
		} else if cross < 0 {
			s = -1
		}
		if s == 0 {
			continue
		}
		if sign == 0 {
			sign = s
		} else if s != sign {
			return false
		}
	}
	return true
}

// thumbGlyph draws a glyph ("play", "pause", "prev" or "next") at size×size
// pixels in colour c, anti-aliased with 4×4 samples a pixel.
func thumbGlyph(kind string, size int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	shapes := glyphShapes[kind]
	const ss = 4
	scale := 16 / float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			hits := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := glyphPoint{(float64(x) + (float64(sx)+0.5)/ss) * scale, (float64(y) + (float64(sy)+0.5)/ss) * scale}
					for _, poly := range shapes {
						if inConvex(poly, p) {
							hits++
							break
						}
					}
				}
			}
			if hits > 0 {
				a := uint32(c.A) * uint32(hits) / (ss * ss)
				img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, uint8(a)})
			}
		}
	}
	return img
}
