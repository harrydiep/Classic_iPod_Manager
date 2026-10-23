package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strconv"
	"strings"
)

// FLAC files start with "fLaC" and a chain of metadata blocks: a header
// byte (last-block flag + type) and a 24-bit length, then the payload.
// Tags live in the VORBIS_COMMENT block (type 4) and pictures in PICTURE
// blocks (type 6). Audio frames follow the last block.

const (
	flacStreamInfo    = 0
	flacPadding       = 1
	flacVorbisComment = 4
	flacPicture       = 6
)

type flacBlock struct {
	typ  byte
	data []byte
}

// readFLACBlocks returns the metadata blocks and where the audio begins.
func readFLACBlocks(f io.ReaderAt) ([]flacBlock, int64, error) {
	var magic [4]byte
	if _, err := f.ReadAt(magic[:], 0); err != nil || string(magic[:]) != "fLaC" {
		return nil, 0, errors.New("not a FLAC file")
	}
	var blocks []flacBlock
	off := int64(4)
	for {
		var h [4]byte
		if _, err := f.ReadAt(h[:], off); err != nil {
			return nil, 0, fmt.Errorf("corrupt FLAC metadata: %w", err)
		}
		last := h[0]&0x80 != 0
		typ := h[0] & 0x7F
		n := int(h[1])<<16 | int(h[2])<<8 | int(h[3])
		if typ == 127 || n > 16<<20 {
			return nil, 0, errors.New("corrupt FLAC metadata")
		}
		data := make([]byte, n)
		if _, err := f.ReadAt(data, off+4); err != nil {
			return nil, 0, fmt.Errorf("corrupt FLAC metadata: %w", err)
		}
		blocks = append(blocks, flacBlock{typ, data})
		off += 4 + int64(n)
		if last {
			break
		}
		if len(blocks) > 4096 {
			return nil, 0, errors.New("corrupt FLAC metadata")
		}
	}
	if len(blocks) == 0 || blocks[0].typ != flacStreamInfo || len(blocks[0].data) < 18 {
		return nil, 0, errors.New("FLAC file has no STREAMINFO block")
	}
	return blocks, off, nil
}

// flacStream decodes STREAMINFO: sample rate, channels, bits and length.
func flacStream(si []byte) (rate, channels, bits int, samples uint64) {
	v := binary.BigEndian.Uint64(si[10:18])
	rate = int(v >> 44)
	channels = int(v>>41&7) + 1
	bits = int(v>>36&31) + 1
	samples = v & (1<<36 - 1)
	return
}

// vorbisFields maps tag fields to Vorbis comment names (upper case).
var vorbisManaged = map[string]bool{"TITLE": true, "ARTIST": true, "ALBUM": true, "ALBUMARTIST": true,
	"ALBUM ARTIST": true, "GENRE": true, "COMPOSER": true, "COMMENT": true, "DESCRIPTION": true,
	"GROUPING": true, "CONTENTGROUP": true, "DATE": true, "YEAR": true, "TRACKNUMBER": true,
	"TRACKTOTAL": true, "TOTALTRACKS": true, "DISCNUMBER": true, "DISCTOTAL": true, "TOTALDISCS": true,
	"BPM": true, "COMPILATION": true}

// VorbisComments returns the Vorbis comment fields for tags, in a stable
// order (also used for Ogg encoders' command lines).
func VorbisComments(t Tags) [][2]string {
	var out [][2]string
	add := func(k, v string) {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, [2]string{k, v})
		}
	}
	num := func(k string, v int) {
		if v > 0 {
			add(k, strconv.Itoa(v))
		}
	}
	add("TITLE", t.Title)
	add("ARTIST", t.Artist)
	add("ALBUM", t.Album)
	add("ALBUMARTIST", t.AlbumArtist)
	add("GENRE", t.Genre)
	add("COMPOSER", t.Composer)
	add("GROUPING", t.Grouping)
	num("DATE", t.Year)
	num("TRACKNUMBER", t.Track)
	num("TRACKTOTAL", t.TrackTotal)
	num("DISCNUMBER", t.Disc)
	num("DISCTOTAL", t.DiscTotal)
	num("BPM", t.BPM)
	if t.Compilation {
		add("COMPILATION", "1")
	}
	add("COMMENT", t.Comment)
	return out
}

func buildVorbisComment(old []byte, t Tags) []byte {
	vendor := "Classic iPod Manager"
	var keep []string
	if len(old) >= 8 {
		le := binary.LittleEndian
		vl := int(le.Uint32(old))
		if 4+vl+4 <= len(old) {
			vendor = string(old[4 : 4+vl])
			n := int(le.Uint32(old[4+vl:]))
			p := 8 + vl
			for i := 0; i < n && p+4 <= len(old); i++ {
				l := int(le.Uint32(old[p:]))
				if p+4+l > len(old) {
					break
				}
				c := string(old[p+4 : p+4+l])
				p += 4 + l
				k, _, _ := strings.Cut(c, "=")
				if !vorbisManaged[strings.ToUpper(k)] {
					keep = append(keep, c)
				}
			}
		}
	}
	var fields []string
	for _, kv := range VorbisComments(t) {
		fields = append(fields, kv[0]+"="+kv[1])
	}
	fields = append(fields, keep...)
	var b bytes.Buffer
	w32 := func(v int) { binary.Write(&b, binary.LittleEndian, uint32(v)) }
	w32(len(vendor))
	b.WriteString(vendor)
	w32(len(fields))
	for _, f := range fields {
		w32(len(f))
		b.WriteString(f)
	}
	return b.Bytes()
}

// flacPictureBlock builds a PICTURE block payload (front cover).
func flacPictureBlock(p *Picture) []byte {
	var b bytes.Buffer
	w32 := func(v int) { binary.Write(&b, binary.BigEndian, uint32(v)) }
	mime := p.mime()
	w, h, depth := 0, 0, 24
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(p.Data)); err == nil {
		w, h = cfg.Width, cfg.Height
	}
	w32(3) // front cover
	w32(len(mime))
	b.WriteString(mime)
	w32(0) // description
	w32(w)
	w32(h)
	w32(depth)
	w32(0)
	w32(len(p.Data))
	b.Write(p.Data)
	return b.Bytes()
}

func writeFLACTags(path string, t Tags, art ArtEdit) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	blocks, audioStart, err := readFLACBlocks(f)
	f.Close()
	if err != nil {
		return err
	}
	var oldComment []byte
	var out []flacBlock
	for _, b := range blocks {
		switch {
		case b.typ == flacPadding:
		case b.typ == flacVorbisComment:
			oldComment = b.data
		case b.typ == flacPicture && art.active():
		default:
			out = append(out, b)
		}
	}
	// STREAMINFO stays first; the comment and picture follow it.
	extra := []flacBlock{{flacVorbisComment, buildVorbisComment(oldComment, t)}}
	if art.Set != nil {
		pb := flacPictureBlock(art.Set)
		if len(pb) >= 1<<24 {
			return errors.New("the picture is too large for a FLAC file")
		}
		extra = append(extra, flacBlock{flacPicture, pb})
	}
	out = append(out[:1], append(extra, out[1:]...)...)

	size := int64(4)
	for _, b := range out {
		size += 4 + int64(len(b.data))
	}
	// Reuse the existing space when it fits (the remainder becomes padding),
	// otherwise rewrite the file with fresh padding for future edits.
	pad := audioStart - size - 4
	inPlace := pad >= 0
	if !inPlace {
		pad = 4096
	}
	out = append(out, flacBlock{flacPadding, make([]byte, pad)})
	var meta bytes.Buffer
	meta.WriteString("fLaC")
	for i, b := range out {
		h := byte(b.typ)
		if i == len(out)-1 {
			h |= 0x80
		}
		n := len(b.data)
		meta.Write([]byte{h, byte(n >> 16), byte(n >> 8), byte(n)})
		meta.Write(b.data)
	}
	if inPlace {
		w, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(meta.Bytes(), 0); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	return rewriteWithPrefix(path, meta.Bytes(), audioStart, nil)
}

// flacPicture returns the front cover (or first picture) of a FLAC file.
func flacPictureOf(f io.ReaderAt) *Picture {
	blocks, _, err := readFLACBlocks(f)
	if err != nil {
		return nil
	}
	var first *Picture
	for _, b := range blocks {
		if b.typ != flacPicture {
			continue
		}
		pic, typ := parseFLACPicture(b.data)
		if pic == nil {
			continue
		}
		if typ == 3 {
			return pic
		}
		if first == nil {
			first = pic
		}
	}
	return first
}

func parseFLACPicture(d []byte) (*Picture, uint32) {
	be := binary.BigEndian
	if len(d) < 32 {
		return nil, 0
	}
	typ := be.Uint32(d)
	p := 4
	ml := int(be.Uint32(d[p:]))
	p += 4
	if p+ml+4 > len(d) {
		return nil, 0
	}
	mime := string(d[p : p+ml])
	p += ml
	dl := int(be.Uint32(d[p:]))
	p += 4 + dl + 16
	if p+4 > len(d) {
		return nil, 0
	}
	n := int(be.Uint32(d[p:]))
	p += 4
	if p+n > len(d) {
		return nil, 0
	}
	data := d[p : p+n]
	if m := sniffImage(data); m != "" {
		mime = m
	}
	return &Picture{Data: data, MIME: mime}, typ
}

// ------------------------------------------------------------- Ogg

// oggInfo reads the stream type, sample rate, channels, nominal bitrate and
// length of an Ogg Vorbis/Opus/FLAC file.
func oggInfo(f *os.File, size int64) (codec string, rate, channels int, bitrate uint32, lengthMs uint32) {
	head := make([]byte, 4096)
	n, _ := f.ReadAt(head, 0)
	head = head[:n]
	if len(head) < 28 || string(head[:4]) != "OggS" {
		return
	}
	segs := int(head[26])
	p := 27 + segs
	if p+20 > len(head) {
		return
	}
	pkt := head[p:]
	switch {
	case len(pkt) >= 30 && pkt[0] == 1 && string(pkt[1:7]) == "vorbis":
		codec = "vorbis"
		channels = int(pkt[11])
		rate = int(binary.LittleEndian.Uint32(pkt[12:]))
		nominal := int32(binary.LittleEndian.Uint32(pkt[20:]))
		if nominal > 0 {
			bitrate = uint32(nominal / 1000)
		}
	case len(pkt) >= 19 && string(pkt[:8]) == "OpusHead":
		codec = "opus"
		channels = int(pkt[9])
		rate = 48000 // Opus granule positions always count 48 kHz samples
	case len(pkt) >= 13 && pkt[0] == 0x7F && string(pkt[1:5]) == "FLAC":
		codec = "flac"
		if len(pkt) >= 13+4+18 {
			r, c, _, _ := flacStream(pkt[13+4:])
			rate, channels = r, c
		}
	default:
		return
	}
	// The last page's granule position is the stream length in samples.
	tailLen := int64(65536)
	if tailLen > size {
		tailLen = size
	}
	tail := make([]byte, tailLen)
	f.ReadAt(tail, size-tailLen)
	if i := bytes.LastIndex(tail, []byte("OggS")); i >= 0 && i+14 <= len(tail) && rate > 0 {
		g := binary.LittleEndian.Uint64(tail[i+6:])
		if g > 0 && g < 1<<50 {
			if codec == "opus" && len(pkt) >= 12 {
				pre := uint64(binary.LittleEndian.Uint16(pkt[10:]))
				if g > pre {
					g -= pre
				}
			}
			lengthMs = uint32(g * 1000 / uint64(rate))
		}
	}
	if lengthMs > 0 {
		avg := uint32(size * 8 / int64(lengthMs))
		if bitrate == 0 || codec == "opus" {
			bitrate = avg
		}
	}
	return
}
