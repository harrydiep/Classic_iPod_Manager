package media

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Ogg Vorbis and Opus keep their tags in the second header packet (a
// Vorbis comment). Changing it means re-packing the header pages and
// renumbering (and re-checksumming) every page after them.

var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = r<<1 ^ 0x04C11DB7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRC(b []byte) uint32 {
	var crc uint32
	for _, c := range b {
		crc = crc<<8 ^ oggCRCTable[byte(crc>>24)^c]
	}
	return crc
}

type oggPage struct {
	headerType byte
	granule    uint64
	serial     uint32
	seq        uint32
	segs       []byte
	data       []byte
}

func readOggPage(r *bufio.Reader) (*oggPage, error) {
	var h [27]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	if string(h[:4]) != "OggS" || h[4] != 0 {
		return nil, errors.New("corrupt Ogg page")
	}
	p := &oggPage{headerType: h[5], granule: binary.LittleEndian.Uint64(h[6:]),
		serial: binary.LittleEndian.Uint32(h[14:]), seq: binary.LittleEndian.Uint32(h[18:])}
	p.segs = make([]byte, h[26])
	if _, err := io.ReadFull(r, p.segs); err != nil {
		return nil, err
	}
	n := 0
	for _, s := range p.segs {
		n += int(s)
	}
	p.data = make([]byte, n)
	if _, err := io.ReadFull(r, p.data); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *oggPage) bytes() []byte {
	b := make([]byte, 27+len(p.segs)+len(p.data))
	copy(b, "OggS")
	b[5] = p.headerType
	binary.LittleEndian.PutUint64(b[6:], p.granule)
	binary.LittleEndian.PutUint32(b[14:], p.serial)
	binary.LittleEndian.PutUint32(b[18:], p.seq)
	b[26] = byte(len(p.segs))
	copy(b[27:], p.segs)
	copy(b[27+len(p.segs):], p.data)
	binary.LittleEndian.PutUint32(b[22:], oggCRC(b))
	return b
}

// packPages lays packets out on pages (each packet ends a page, as header
// packets must).
func packPages(packets [][]byte, serial uint32, seq uint32) []*oggPage {
	var pages []*oggPage
	for _, pkt := range packets {
		// lacing values: 255s then the remainder (0 if a multiple of 255)
		var lace []byte
		n := len(pkt)
		for n >= 255 {
			lace = append(lace, 255)
			n -= 255
		}
		lace = append(lace, byte(n))
		off := 0
		first := true
		for len(lace) > 0 {
			k := len(lace)
			if k > 255 {
				k = 255
			}
			size := 0
			for _, s := range lace[:k] {
				size += int(s)
			}
			p := &oggPage{serial: serial, seq: seq, segs: append([]byte(nil), lace[:k]...), data: pkt[off : off+size]}
			if !first {
				p.headerType |= 0x01 // continued packet
			}
			pages = append(pages, p)
			seq++
			off += size
			lace = lace[k:]
			first = false
		}
	}
	return pages
}

// oggCommentPacket builds a Vorbis comment packet for Vorbis or Opus.
func oggCommentPacket(old []byte, opus bool, t Tags, art ArtEdit) []byte {
	prefix := []byte("\x03vorbis")
	if opus {
		prefix = []byte("OpusTags")
	}
	body := old
	if len(body) >= len(prefix) {
		body = body[len(prefix):]
	}
	// reuse the FLAC Vorbis comment builder, then add pictures
	vc := buildVorbisComment(body, t)
	le := binary.LittleEndian
	vl := int(le.Uint32(vc))
	n := int(le.Uint32(vc[4+vl:]))
	fields := vc[8+vl:]
	var keep [][]byte
	// drop pictures from the kept fields when the art changes
	for p := 0; p+4 <= len(fields); {
		l := int(le.Uint32(fields[p:]))
		c := fields[p+4 : p+4+l]
		k, _, _ := strings.Cut(string(c), "=")
		if !(art.active() && (strings.EqualFold(k, "METADATA_BLOCK_PICTURE") || strings.EqualFold(k, "COVERART"))) {
			keep = append(keep, c)
		}
		p += 4 + l
	}
	_ = n
	if art.Set != nil {
		keep = append(keep, []byte("METADATA_BLOCK_PICTURE="+base64.StdEncoding.EncodeToString(flacPictureBlock(art.Set))))
	}
	var b bytes.Buffer
	b.Write(prefix)
	b.Write(vc[:4+vl])
	binary.Write(&b, binary.LittleEndian, uint32(len(keep)))
	for _, c := range keep {
		binary.Write(&b, binary.LittleEndian, uint32(len(c)))
		b.Write(c)
	}
	if !opus {
		b.WriteByte(1) // framing bit
	}
	return b.Bytes()
}

func writeOggTags(path string, t Tags, art ArtEdit) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	r := bufio.NewReaderSize(in, 1<<16)
	first, err := readOggPage(r)
	if err != nil {
		return err
	}
	if first.headerType&0x02 == 0 {
		return errors.New("corrupt Ogg file")
	}
	serial := first.serial
	var headers int
	var opus bool
	switch {
	case bytes.HasPrefix(first.data, []byte("\x01vorbis")):
		headers = 3
	case bytes.HasPrefix(first.data, []byte("OpusHead")):
		headers, opus = 2, true
	default:
		return errNoTagSupport
	}
	// Gather the header packets; they end on a page boundary.
	var packets [][]byte
	var cur []byte
	pages := []*oggPage{first}
	headerPages := 0
	for p := first; ; {
		if p.serial == serial {
			off := 0
			for _, s := range p.segs {
				cur = append(cur, p.data[off:off+int(s)]...)
				off += int(s)
				if s < 255 {
					packets = append(packets, cur)
					cur = nil
				}
			}
		}
		headerPages++
		if len(packets) >= headers {
			if len(packets) > headers || cur != nil {
				return errors.New("unusual Ogg header layout")
			}
			break
		}
		if p, err = readOggPage(r); err != nil {
			return errors.New("corrupt Ogg headers")
		}
		if p.serial != serial {
			return errors.New("Ogg files with several streams aren't supported")
		}
		pages = append(pages, p)
	}
	packets[1] = oggCommentPacket(packets[1], opus, t, art)
	newPages := packPages(packets, serial, first.seq)
	newPages[0].headerType = 0x02 // beginning of stream
	delta := int64(len(newPages)) - int64(headerPages)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".podmgr-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()
	w := bufio.NewWriterSize(tmp, 1<<20)
	for _, p := range newPages {
		if _, err := w.Write(p.bytes()); err != nil {
			return err
		}
	}
	for {
		p, err := readOggPage(r)
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if p.serial == serial {
			p.seq = uint32(int64(p.seq) + delta)
		}
		if _, err := w.Write(p.bytes()); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	in.Close()
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// oggPicture reads METADATA_BLOCK_PICTURE from an Ogg Vorbis/Opus file.
func oggPicture(f *os.File) *Picture {
	f.Seek(0, io.SeekStart)
	r := bufio.NewReaderSize(f, 1<<16)
	var pkt []byte
	n := 0
	for pages := 0; pages < 64; pages++ {
		p, err := readOggPage(r)
		if err != nil {
			return nil
		}
		off := 0
		for _, s := range p.segs {
			pkt = append(pkt, p.data[off:off+int(s)]...)
			off += int(s)
			if s < 255 {
				n++
				if n == 2 {
					return pictureFromComment(pkt)
				}
				pkt = nil
			}
		}
	}
	return nil
}

func pictureFromComment(pkt []byte) *Picture {
	switch {
	case bytes.HasPrefix(pkt, []byte("\x03vorbis")):
		pkt = pkt[7:]
	case bytes.HasPrefix(pkt, []byte("OpusTags")):
		pkt = pkt[8:]
	default:
		return nil
	}
	le := binary.LittleEndian
	if len(pkt) < 8 {
		return nil
	}
	vl := int(le.Uint32(pkt))
	if 8+vl > len(pkt) {
		return nil
	}
	n := int(le.Uint32(pkt[4+vl:]))
	p := 8 + vl
	var first *Picture
	for i := 0; i < n && p+4 <= len(pkt); i++ {
		l := int(le.Uint32(pkt[p:]))
		if p+4+l > len(pkt) {
			break
		}
		k, v, _ := strings.Cut(string(pkt[p+4:p+4+l]), "=")
		p += 4 + l
		if !strings.EqualFold(k, "METADATA_BLOCK_PICTURE") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			continue
		}
		pic, typ := parseFLACPicture(raw)
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
