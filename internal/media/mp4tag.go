package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// writeMP4Tags rewrites the iTunes-style metadata (moov/udta/meta/ilst) of
// an .m4a/.m4b file. Unmanaged items (cover art, iTunNORM, …) are kept.
// When the movie header sits in front of the audio data, chunk offsets are
// adjusted (or a following 'free' atom absorbs the size change).
func writeMP4Tags(path string, t Tags) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	size := st.Size()
	top, err := readAtoms(f, 0, size)
	if err != nil {
		f.Close()
		return err
	}
	mi := -1
	for i, a := range top {
		if a.typ == "moov" {
			mi = i
		}
	}
	if mi < 0 {
		f.Close()
		return fmt.Errorf("no moov atom")
	}
	moov := top[mi]
	raw := make([]byte, moov.size)
	if _, err := f.ReadAt(raw, moov.off); err != nil {
		f.Close()
		return err
	}
	f.Close()

	newMoov, err := rebuildMoov(raw, moov.headerSize, t)
	if err != nil {
		return err
	}
	delta := int64(len(newMoov)) - moov.size

	// Is any media data located after the moov atom?
	dataAfter := false
	for _, a := range top[mi+1:] {
		if a.typ == "mdat" {
			dataAfter = true
		}
	}
	// Try to absorb the change with an adjacent free atom.
	var freeAfter *atom
	if mi+1 < len(top) && (top[mi+1].typ == "free" || top[mi+1].typ == "skip") && top[mi+1].headerSize == 8 {
		freeAfter = &top[mi+1]
	}
	if delta != 0 && dataAfter && freeAfter != nil && freeAfter.size-delta >= 8 {
		nf := freeAfter.size - delta
		fb := make([]byte, nf)
		binary.BigEndian.PutUint32(fb, uint32(nf))
		copy(fb[4:], "free")
		w, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(append(newMoov, fb...), moov.off); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	if delta == 0 {
		w, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(newMoov, moov.off); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	if dataAfter {
		if err := shiftChunkOffsets(newMoov, moov.off+moov.size, delta); err != nil {
			return err
		}
	}
	return spliceFile(path, moov.off, moov.size, newMoov)
}

// spliceFile replaces [off, off+oldLen) of the file with repl via a temp file.
func spliceFile(path string, off, oldLen int64, repl []byte) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(filepath.Dir(path), ".podmgr-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if _, err := io.Copy(tmp, io.NewSectionReader(src, 0, off)); err != nil {
		return err
	}
	if _, err := tmp.Write(repl); err != nil {
		return err
	}
	if _, err := src.Seek(off+oldLen, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	src.Close()
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// box helpers ---------------------------------------------------------

type box struct {
	typ  string
	data []byte // payload (without 8 byte header)
}

func parseBoxes(b []byte) ([]box, error) {
	var out []box
	for off := 0; off+8 <= len(b); {
		sz := int(binary.BigEndian.Uint32(b[off:]))
		typ := string(b[off+4 : off+8])
		hs := 8
		if sz == 1 {
			if off+16 > len(b) {
				return nil, fmt.Errorf("bad box")
			}
			sz = int(binary.BigEndian.Uint64(b[off+8:]))
			hs = 16
		} else if sz == 0 {
			sz = len(b) - off
		}
		if sz < hs || off+sz > len(b) {
			return nil, fmt.Errorf("corrupt atom %q", typ)
		}
		out = append(out, box{typ, b[off+hs : off+sz]})
		off += sz
	}
	return out, nil
}

func encodeBox(typ string, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:], typ)
	return append(b, payload...)
}

func encodeBoxes(list []box) []byte {
	var b bytes.Buffer
	for _, x := range list {
		b.Write(encodeBox(x.typ, x.data))
	}
	return b.Bytes()
}

func rebuildMoov(raw []byte, hs int64, t Tags) ([]byte, error) {
	kids, err := parseBoxes(raw[hs:])
	if err != nil {
		return nil, err
	}
	ui := -1
	for i, k := range kids {
		if k.typ == "udta" {
			ui = i
		}
	}
	var udta []box
	if ui >= 0 {
		if udta, err = parseBoxes(kids[ui].data); err != nil {
			return nil, err
		}
	}
	mi := -1
	for i, k := range udta {
		if k.typ == "meta" {
			mi = i
		}
	}
	var metaKids []box
	metaVF := []byte{0, 0, 0, 0}
	if mi >= 0 && len(udta[mi].data) >= 4 {
		metaVF = udta[mi].data[:4]
		if metaKids, err = parseBoxes(udta[mi].data[4:]); err != nil {
			return nil, err
		}
	}
	hasHdlr := false
	ii := -1
	for i, k := range metaKids {
		if k.typ == "hdlr" {
			hasHdlr = true
		}
		if k.typ == "ilst" {
			ii = i
		}
	}
	if !hasHdlr {
		h := make([]byte, 25)
		copy(h[8:], "mdir")
		copy(h[12:], "appl")
		metaKids = append([]box{{"hdlr", h}}, metaKids...)
		if ii >= 0 {
			ii++
		}
	}
	var items []box
	if ii >= 0 {
		if items, err = parseBoxes(metaKids[ii].data); err != nil {
			return nil, err
		}
	}
	items = applyMP4(items, t)
	ilst := box{"ilst", encodeBoxes(items)}
	if ii >= 0 {
		metaKids[ii] = ilst
	} else {
		// put ilst right after hdlr
		var nk []box
		for _, k := range metaKids {
			nk = append(nk, k)
			if k.typ == "hdlr" {
				nk = append(nk, ilst)
			}
		}
		metaKids = nk
	}
	meta := box{"meta", append(append([]byte(nil), metaVF...), encodeBoxes(metaKids)...)}
	if mi >= 0 {
		udta[mi] = meta
	} else {
		udta = append(udta, meta)
	}
	nu := box{"udta", encodeBoxes(udta)}
	if ui >= 0 {
		kids[ui] = nu
	} else {
		kids = append(kids, nu)
	}
	return encodeBox("moov", encodeBoxes(kids)), nil
}

func dataBox(typ uint32, payload []byte) []byte {
	b := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(b, typ)
	return encodeBox("data", append(b, payload...))
}

func applyMP4(items []box, t Tags) []box {
	managed := map[string]bool{"\xa9nam": true, "\xa9ART": true, "\xa9alb": true, "aART": true, "\xa9gen": true,
		"gnre": true, "\xa9wrt": true, "\xa9day": true, "trkn": true, "disk": true, "\xa9cmt": true,
		"cpil": true, "tmpo": true, "\xa9grp": true}
	var out []box
	str := func(typ, v string) {
		if v != "" {
			out = append(out, box{typ, dataBox(1, []byte(v))})
		}
	}
	str("\xa9nam", t.Title)
	str("\xa9ART", t.Artist)
	str("aART", t.AlbumArtist)
	str("\xa9alb", t.Album)
	str("\xa9grp", t.Grouping)
	str("\xa9gen", t.Genre)
	str("\xa9wrt", t.Composer)
	if t.Year > 0 {
		str("\xa9day", fmt.Sprintf("%d", t.Year))
	}
	if t.Track > 0 || t.TrackTotal > 0 {
		p := make([]byte, 8)
		binary.BigEndian.PutUint16(p[2:], uint16(t.Track))
		binary.BigEndian.PutUint16(p[4:], uint16(t.TrackTotal))
		out = append(out, box{"trkn", dataBox(0, p)})
	}
	if t.Disc > 0 || t.DiscTotal > 0 {
		p := make([]byte, 6)
		binary.BigEndian.PutUint16(p[2:], uint16(t.Disc))
		binary.BigEndian.PutUint16(p[4:], uint16(t.DiscTotal))
		out = append(out, box{"disk", dataBox(0, p)})
	}
	if t.BPM > 0 {
		p := make([]byte, 2)
		binary.BigEndian.PutUint16(p, uint16(t.BPM))
		out = append(out, box{"tmpo", dataBox(21, p)})
	}
	if t.Compilation {
		out = append(out, box{"cpil", dataBox(21, []byte{1})})
	}
	str("\xa9cmt", t.Comment)
	for _, it := range items {
		if !managed[it.typ] {
			out = append(out, it)
		}
	}
	return out
}

// shiftChunkOffsets adds delta to every stco/co64 entry that points at or
// beyond 'from' inside a serialised moov atom (modified in place).
func shiftChunkOffsets(moov []byte, from, delta int64) error {
	var walk func(b []byte) error
	walk = func(b []byte) error {
		for off := 0; off+8 <= len(b); {
			sz := int(binary.BigEndian.Uint32(b[off:]))
			typ := string(b[off+4 : off+8])
			hs := 8
			if sz == 1 {
				sz = int(binary.BigEndian.Uint64(b[off+8:]))
				hs = 16
			}
			if sz < hs || off+sz > len(b) {
				return fmt.Errorf("corrupt atom %q", typ)
			}
			body := b[off+hs : off+sz]
			switch typ {
			case "trak", "mdia", "minf", "stbl", "edts", "dinf":
				if err := walk(body); err != nil {
					return err
				}
			case "stco":
				if len(body) >= 8 {
					n := int(binary.BigEndian.Uint32(body[4:]))
					for i := 0; i < n && 8+4*i+4 <= len(body); i++ {
						p := body[8+4*i:]
						v := int64(binary.BigEndian.Uint32(p))
						if v >= from {
							v += delta
							if v > 0xFFFFFFFF || v < 0 {
								return fmt.Errorf("chunk offset overflow")
							}
							binary.BigEndian.PutUint32(p, uint32(v))
						}
					}
				}
			case "co64":
				if len(body) >= 8 {
					n := int(binary.BigEndian.Uint32(body[4:]))
					for i := 0; i < n && 8+8*i+8 <= len(body); i++ {
						p := body[8+8*i:]
						v := int64(binary.BigEndian.Uint64(p))
						if v >= from {
							binary.BigEndian.PutUint64(p, uint64(v+delta))
						}
					}
				}
			}
			off += sz
		}
		return nil
	}
	return walk(moov[8:])
}

// WriteTags writes metadata into an audio file. WAV and AIFF files are not
// supported (the iPod database still holds the edited values).
func WriteTags(path string, t Tags) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	var magic [12]byte
	n, _ := io.ReadFull(f, magic[:])
	f.Close()
	if n < 12 {
		return errNoTagSupport
	}
	switch {
	case string(magic[4:8]) == "ftyp":
		return writeMP4Tags(path, t)
	case string(magic[0:4]) == "RIFF", string(magic[0:4]) == "FORM":
		return errNoTagSupport
	default:
		return writeMP3Tags(path, t)
	}
}

// IsNoTagSupport reports whether err means the format can't hold tags.
func IsNoTagSupport(err error) bool { return err == errNoTagSupport }

// mp4ItemData returns the payload of the first 'data' box of an ilst item.
func mp4ItemData(f *os.File, item string) ([]byte, bool) {
	st, err := f.Stat()
	if err != nil {
		return nil, false
	}
	top, _ := readAtoms(f, 0, st.Size())
	moov := findAtom(top, "moov")
	if moov == nil || moov.size > 64<<20 {
		return nil, false
	}
	raw := make([]byte, moov.size)
	if _, err := f.ReadAt(raw, moov.off); err != nil {
		return nil, false
	}
	path := []string{"udta", "meta", "ilst", item, "data"}
	cur := raw[moov.headerSize:]
	for _, name := range path {
		kids, err := parseBoxes(cur)
		if err != nil {
			return nil, false
		}
		found := false
		for _, k := range kids {
			if k.typ == name {
				cur = k.data
				if name == "meta" {
					if len(cur) < 4 {
						return nil, false
					}
					cur = cur[4:]
				}
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	if len(cur) < 8 {
		return nil, false
	}
	return cur[8:], true
}
