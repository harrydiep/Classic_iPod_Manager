package media

import (
	"bytes"
	"io"
	"os"
)

// Picture is an embedded cover image.
type Picture struct {
	Data []byte
	MIME string
}

// ReadArtwork returns the cover image embedded in an audio file (the ID3
// "front cover" picture for MP3s, the 'covr' atom for MP4s), or nil.
func ReadArtwork(path string) (*Picture, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var magic [12]byte
	if n, _ := io.ReadFull(f, magic[:]); n < 12 {
		return nil, nil
	}
	switch {
	case string(magic[4:8]) == "ftyp":
		if d, ok := mp4ItemData(f, "covr"); ok && len(d) > 8 {
			return &Picture{Data: d, MIME: sniffImage(d)}, nil
		}
		return nil, nil
	case string(magic[0:3]) == "ID3":
		size := id3v2Size(magic[:10])
		if size <= 10 || size > 64<<20 {
			return nil, nil
		}
		raw := make([]byte, size)
		if _, err := f.ReadAt(raw, 0); err != nil {
			return nil, nil
		}
		frames, ver := parseID3Frames(raw)
		var first *Picture
		for _, fr := range frames {
			if fr.id != "APIC" {
				continue
			}
			data := fr.data
			if ver == 4 {
				if fr.flags[1]&0x01 != 0 && len(data) >= 4 { // data length indicator
					data = data[4:]
				}
				if fr.flags[1]&0x02 != 0 {
					data = unsync(data)
				}
			}
			pic, typ := parseAPIC(data)
			if pic == nil {
				continue
			}
			if typ == 3 { // front cover
				return pic, nil
			}
			if first == nil {
				first = pic
			}
		}
		return first, nil
	}
	return nil, nil
}

func parseAPIC(d []byte) (*Picture, byte) {
	if len(d) < 4 {
		return nil, 0
	}
	enc := d[0]
	i := bytes.IndexByte(d[1:], 0)
	if i < 0 {
		return nil, 0
	}
	mime := string(d[1 : 1+i])
	p := 1 + i + 1
	if p >= len(d) {
		return nil, 0
	}
	typ := d[p]
	p++
	// skip the description
	if enc == 1 || enc == 2 {
		for p+1 < len(d) && !(d[p] == 0 && d[p+1] == 0) {
			p += 2
		}
		p += 2
	} else {
		for p < len(d) && d[p] != 0 {
			p++
		}
		p++
	}
	if p >= len(d) {
		return nil, 0
	}
	data := d[p:]
	if m := sniffImage(data); m != "" {
		mime = m
	}
	if mime == "" || mime == "-->" {
		return nil, 0
	}
	return &Picture{Data: data, MIME: mime}, typ
}

func sniffImage(d []byte) string {
	switch {
	case len(d) > 3 && d[0] == 0xFF && d[1] == 0xD8:
		return "image/jpeg"
	case len(d) > 8 && bytes.HasPrefix(d, []byte("\x89PNG")):
		return "image/png"
	case len(d) > 6 && bytes.HasPrefix(d, []byte("GIF8")):
		return "image/gif"
	case len(d) > 2 && d[0] == 'B' && d[1] == 'M':
		return "image/bmp"
	}
	return ""
}
