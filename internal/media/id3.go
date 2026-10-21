package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

type id3Frame struct {
	id    string
	flags [2]byte
	data  []byte
}

// writeMP3Tags rewrites the ID3v2 tag of an MP3 file, keeping every frame it
// does not manage (artwork, ReplayGain, iTunes data…) and updates an
// existing ID3v1 tag.
func writeMP3Tags(path string, t Tags, art ArtEdit) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	var hdr [10]byte
	n, _ := f.ReadAt(hdr[:], 0)
	oldSize := 0
	version := byte(3)
	var frames []id3Frame
	if n == 10 && string(hdr[:3]) == "ID3" {
		oldSize = id3v2Size(hdr[:])
		if int64(oldSize) > st.Size() {
			f.Close()
			return fmt.Errorf("corrupt ID3 tag")
		}
		raw := make([]byte, oldSize)
		if _, err := f.ReadAt(raw, 0); err != nil {
			f.Close()
			return err
		}
		frames, version = parseID3Frames(raw)
	}
	f.Close()

	frames = applyID3(frames, version, t, art)
	body := encodeID3Frames(frames, version)

	inPlace := oldSize > 0 && len(body)+10 <= oldSize && hdr[5]&0x10 == 0
	tagSize := len(body) + 10
	if !inPlace {
		tagSize = len(body) + 10 + 2048 // padding for future edits
	}
	tag := make([]byte, tagSize)
	if inPlace {
		tag = make([]byte, oldSize)
	}
	copy(tag, "ID3")
	tag[3] = version
	tag[4] = 0
	tag[5] = 0
	putSyncsafe(tag[6:], uint32(len(tag)-10))
	copy(tag[10:], body)

	if inPlace {
		w, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := w.WriteAt(tag, 0); err != nil {
			w.Close()
			return err
		}
		if err := updateID3v1(w, st.Size(), t); err != nil {
			w.Close()
			return err
		}
		return w.Close()
	}
	return rewriteWithPrefix(path, tag, int64(oldSize), func(w *os.File, size int64) error {
		return updateID3v1(w, size, t)
	})
}

// rewriteWithPrefix replaces the first skip bytes of a file with prefix,
// writing to a temporary file in the same folder and renaming it over the
// original.
func rewriteWithPrefix(path string, prefix []byte, skip int64, after func(*os.File, int64) error) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
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
	if _, err := tmp.Write(prefix); err != nil {
		return err
	}
	if _, err := src.Seek(skip, io.SeekStart); err != nil {
		return err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		return err
	}
	if after != nil {
		st, _ := tmp.Stat()
		if err := after(tmp, st.Size()); err != nil {
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	src.Close()
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}

func putSyncsafe(b []byte, v uint32) {
	b[0] = byte(v>>21) & 0x7f
	b[1] = byte(v>>14) & 0x7f
	b[2] = byte(v>>7) & 0x7f
	b[3] = byte(v) & 0x7f
}

func syncsafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

func unsync(b []byte) []byte {
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		out = append(out, b[i])
		if b[i] == 0xFF && i+1 < len(b) && b[i+1] == 0 {
			i++
		}
	}
	return out
}

// parseID3Frames returns the frames of an ID3v2 tag and the version that
// should be written back (3 or 4).
func parseID3Frames(raw []byte) ([]id3Frame, byte) {
	ver := raw[3]
	flags := raw[5]
	body := raw[10:]
	if ver < 4 && flags&0x80 != 0 {
		body = unsync(body)
	}
	if ver == 2 {
		return convertV22(body), 3
	}
	if ver != 3 && ver != 4 {
		return nil, 3
	}
	pos := 0
	if flags&0x40 != 0 && len(body) >= 4 { // extended header
		if ver == 4 {
			pos = syncsafe(body[0:4])
		} else {
			pos = int(binary.BigEndian.Uint32(body[0:4])) + 4
		}
	}
	var frames []id3Frame
	for pos+10 <= len(body) {
		id := string(body[pos : pos+4])
		if body[pos] == 0 || !validFrameID(id) {
			break
		}
		var sz int
		if ver == 4 {
			sz = syncsafe(body[pos+4 : pos+8])
		} else {
			sz = int(binary.BigEndian.Uint32(body[pos+4:]))
		}
		if pos+10+sz > len(body) || sz < 0 {
			break
		}
		fr := id3Frame{id: id, data: append([]byte(nil), body[pos+10:pos+10+sz]...)}
		copy(fr.flags[:], body[pos+8:pos+10])
		frames = append(frames, fr)
		pos += 10 + sz
	}
	return frames, ver
}

func validFrameID(id string) bool {
	for _, c := range []byte(id) {
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

// convertV22 upgrades the few v2.2 frames worth keeping (artwork) – the text
// frames are rewritten from the database anyway.
func convertV22(body []byte) []id3Frame {
	var frames []id3Frame
	for pos := 0; pos+6 <= len(body); {
		id := string(body[pos : pos+3])
		if body[pos] == 0 {
			break
		}
		sz := int(body[pos+3])<<16 | int(body[pos+4])<<8 | int(body[pos+5])
		if pos+6+sz > len(body) {
			break
		}
		d := body[pos+6 : pos+6+sz]
		if id == "PIC" && len(d) > 5 {
			mime := "image/jpeg"
			if strings.EqualFold(string(d[1:4]), "PNG") {
				mime = "image/png"
			}
			nd := []byte{d[0]}
			nd = append(nd, mime...)
			nd = append(nd, 0)
			nd = append(nd, d[4:]...)
			frames = append(frames, id3Frame{id: "APIC", data: nd})
		}
		pos += 6 + sz
	}
	return frames
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func encodeText(s string, ver byte) (enc byte, data []byte) {
	if ver == 4 {
		return 3, []byte(s)
	}
	if isASCII(s) {
		return 0, []byte(s)
	}
	u := utf16.Encode([]rune(s))
	b := []byte{0xFF, 0xFE}
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return 1, b
}

func textFrame(id, s string, ver byte) id3Frame {
	enc, d := encodeText(s, ver)
	return id3Frame{id: id, data: append([]byte{enc}, d...)}
}

func commFrame(s string, ver byte) id3Frame {
	enc, d := encodeText(s, ver)
	b := []byte{enc, 'e', 'n', 'g'}
	if enc == 1 {
		b = append(b, 0xFF, 0xFE, 0, 0)
	} else {
		b = append(b, 0)
	}
	return id3Frame{id: "COMM", data: append(b, d...)}
}

// commIsPlain reports whether a COMM frame has an empty description (the
// user-visible comment, as opposed to iTunNORM & co).
func commIsPlain(d []byte) bool {
	if len(d) < 5 {
		return true
	}
	enc := d[0]
	desc := d[4:]
	switch enc {
	case 1, 2:
		if len(desc) >= 2 && ((desc[0] == 0xFF && desc[1] == 0xFE) || (desc[0] == 0xFE && desc[1] == 0xFF)) {
			desc = desc[2:]
		}
		return len(desc) >= 2 && desc[0] == 0 && desc[1] == 0
	default:
		return len(desc) >= 1 && desc[0] == 0
	}
}

func pair(n, total int) string {
	if n <= 0 {
		return ""
	}
	if total > 0 {
		return strconv.Itoa(n) + "/" + strconv.Itoa(total)
	}
	return strconv.Itoa(n)
}

func applyID3(frames []id3Frame, ver byte, t Tags, art ArtEdit) []id3Frame {
	managed := map[string]bool{"TIT2": true, "TPE1": true, "TALB": true, "TPE2": true, "TCON": true,
		"TCOM": true, "TIT1": true, "TRCK": true, "TPOS": true, "TYER": true, "TDRC": true, "TBPM": true, "TCMP": true}
	var out []id3Frame
	for _, fr := range frames {
		if managed[fr.id] {
			continue
		}
		if fr.id == "COMM" && commIsPlain(fr.data) {
			continue
		}
		if art.active() && (fr.id == "APIC" || fr.id == "PIC") {
			continue
		}
		out = append(out, fr)
	}
	var add []id3Frame
	set := func(id, v string) {
		if v != "" {
			add = append(add, textFrame(id, v, ver))
		}
	}
	set("TIT2", t.Title)
	set("TPE1", t.Artist)
	set("TALB", t.Album)
	set("TPE2", t.AlbumArtist)
	set("TCON", t.Genre)
	set("TCOM", t.Composer)
	set("TIT1", t.Grouping)
	set("TRCK", pair(t.Track, t.TrackTotal))
	set("TPOS", pair(t.Disc, t.DiscTotal))
	if t.Year > 0 {
		if ver == 4 {
			set("TDRC", strconv.Itoa(t.Year))
		} else {
			set("TYER", strconv.Itoa(t.Year))
		}
	}
	if t.BPM > 0 {
		set("TBPM", strconv.Itoa(t.BPM))
	}
	if t.Compilation {
		set("TCMP", "1")
	}
	if t.Comment != "" {
		add = append(add, commFrame(t.Comment, ver))
	}
	if art.Set != nil {
		// APIC: text encoding, MIME type, picture type 3 (front cover),
		// empty description, picture data.
		d := []byte{0}
		d = append(d, art.Set.mime()...)
		d = append(d, 0, 3, 0)
		d = append(d, art.Set.Data...)
		add = append(add, id3Frame{id: "APIC", data: d})
	}
	return append(add, out...)
}

func encodeID3Frames(frames []id3Frame, ver byte) []byte {
	var b bytes.Buffer
	for _, fr := range frames {
		b.WriteString(fr.id)
		var sz [4]byte
		if ver == 4 {
			putSyncsafe(sz[:], uint32(len(fr.data)))
		} else {
			binary.BigEndian.PutUint32(sz[:], uint32(len(fr.data)))
		}
		b.Write(sz[:])
		b.Write(fr.flags[:])
		b.Write(fr.data)
	}
	return b.Bytes()
}

// updateID3v1 refreshes an existing ID3v1 tag at the end of the file.
func updateID3v1(f *os.File, size int64, t Tags) error {
	if size < 128 {
		return nil
	}
	var tag [128]byte
	if _, err := f.ReadAt(tag[:], size-128); err != nil || string(tag[:3]) != "TAG" {
		return nil
	}
	put := func(off, n int, s string) {
		for i := 0; i < n; i++ {
			tag[off+i] = 0
		}
		var b []byte
		for _, r := range s {
			if r < 256 {
				b = append(b, byte(r))
			} else {
				b = append(b, '?')
			}
			if len(b) == n {
				break
			}
		}
		copy(tag[off:off+n], b)
	}
	put(3, 30, t.Title)
	put(33, 30, t.Artist)
	put(63, 30, t.Album)
	if t.Year > 0 {
		put(93, 4, strconv.Itoa(t.Year))
	} else {
		put(93, 4, "")
	}
	put(97, 28, t.Comment)
	tag[125] = 0
	if t.Track > 0 && t.Track < 256 {
		tag[126] = byte(t.Track)
	} else {
		tag[126] = 0
	}
	_, err := f.WriteAt(tag[:], size-128)
	return err
}
