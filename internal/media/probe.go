// Package media inspects audio files (format, duration, bitrate, tags) and
// rewrites their tags. Supported for playback on the iPod 5G: MP3, AAC
// (.m4a/.m4b), Apple Lossless, WAV and AIFF.
package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dhowden/tag"
)

// Tags is the editable metadata of a song.
type Tags struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	Album       string `json:"album"`
	AlbumArtist string `json:"albumArtist"`
	Genre       string `json:"genre"`
	Composer    string `json:"composer"`
	Comment     string `json:"comment"`
	Grouping    string `json:"grouping"`
	Year        int    `json:"year"`
	Track       int    `json:"trackNumber"`
	TrackTotal  int    `json:"trackCount"`
	Disc        int    `json:"discNumber"`
	DiscTotal   int    `json:"discCount"`
	BPM         int    `json:"bpm"`
	Compilation bool   `json:"compilation"`
}

// Info describes an audio file.
type Info struct {
	Format       string // mp3, aac, alac, wav, aiff
	FileTypeCode string // iTunesDB file type, e.g. "MP3 "
	Kind         string // e.g. "MPEG audio file"
	Ext          string // extension to use on the iPod (with dot)
	LengthMs     uint32
	Bitrate      uint32 // kbps
	SampleRate   uint32
	VBR          bool
	SampleCount  uint64
	Channels     int // 0 if unknown
	Bits         int // bits per sample for PCM and lossless audio, 0 for lossy
	Audiobook    bool
	Size         int64
	Tags         Tags
}

// ErrUnsupported is returned for files the iPod cannot play.
type ErrUnsupported struct{ Reason string }

func (e ErrUnsupported) Error() string { return e.Reason }

// SupportedExt reports whether a file extension is worth probing.
func SupportedExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".m4a", ".m4b", ".mp4", ".aac", ".wav", ".aif", ".aiff", ".aifc":
		return true
	}
	return false
}

// KnownAudioExt reports extensions of audio files we recognise but cannot
// put on an iPod (used for clearer error messages when scanning folders).
func KnownAudioExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".flac", ".ogg", ".oga", ".opus", ".wma", ".ape", ".wv", ".dsf", ".dff":
		return true
	}
	return SupportedExt(path)
}

// Probe inspects an audio file.
func Probe(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > math.MaxUint32 {
		return nil, ErrUnsupported{"file is larger than 4 GB"}
	}
	var magic [12]byte
	n, _ := io.ReadFull(f, magic[:])
	if n < 12 {
		return nil, ErrUnsupported{"file is too small to be audio"}
	}
	info := &Info{Size: st.Size()}
	ext := strings.ToLower(filepath.Ext(path))
	switch {
	case string(magic[4:8]) == "ftyp":
		err = probeMP4(f, info)
		if info.Format == "aac" && ext == ".m4b" {
			info.Audiobook = true
			info.FileTypeCode = "M4B "
			info.Kind = "AAC audio book file"
			info.Ext = ".m4b"
		}
	case string(magic[0:4]) == "RIFF" && string(magic[8:12]) == "WAVE":
		err = probeWAV(f, info)
	case string(magic[0:4]) == "FORM" && (string(magic[8:12]) == "AIFF" || string(magic[8:12]) == "AIFC"):
		err = probeAIFF(f, info)
	case string(magic[0:4]) == "fLaC":
		return nil, ErrUnsupported{"FLAC isn't playable on an iPod classic – convert it to Apple Lossless or AAC first"}
	case string(magic[0:4]) == "OggS":
		return nil, ErrUnsupported{"Ogg/Opus isn't playable on an iPod classic"}
	case magic[0] == 0x30 && magic[1] == 0x26 && magic[2] == 0xB2 && magic[3] == 0x75:
		return nil, ErrUnsupported{"WMA isn't playable on an iPod classic"}
	case string(magic[0:3]) == "ID3" || (magic[0] == 0xFF && magic[1]&0xE0 == 0xE0) || ext == ".mp3":
		err = probeMP3(f, st.Size(), info)
	default:
		return nil, ErrUnsupported{"unrecognised audio format"}
	}
	if err != nil {
		return nil, err
	}
	if info.LengthMs == 0 {
		return nil, ErrUnsupported{"could not determine the song length"}
	}
	if info.Bitrate == 0 && info.LengthMs > 0 {
		info.Bitrate = uint32(float64(info.Size) * 8 / float64(info.LengthMs))
	}
	info.Tags = readTags(f)
	if strings.TrimSpace(info.Tags.Title) == "" {
		base := filepath.Base(path)
		info.Tags.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	return info, nil
}

func readTags(f *os.File) Tags {
	var t Tags
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return t
	}
	m, err := tag.ReadFrom(f)
	if err != nil {
		return t
	}
	t.Title = clean(m.Title())
	t.Artist = clean(m.Artist())
	t.Album = clean(m.Album())
	t.AlbumArtist = clean(m.AlbumArtist())
	t.Genre = clean(m.Genre())
	t.Composer = clean(m.Composer())
	t.Comment = clean(m.Comment())
	t.Year = m.Year()
	t.Track, t.TrackTotal = m.Track()
	t.Disc, t.DiscTotal = m.Disc()
	raw := m.Raw()
	str := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				switch x := v.(type) {
				case string:
					return x
				case int:
					return strconv.Itoa(x)
				case []byte:
					if len(x) == 1 {
						return strconv.Itoa(int(x[0]))
					}
					if len(x) == 2 {
						return strconv.Itoa(int(binary.BigEndian.Uint16(x)))
					}
				case bool:
					if x {
						return "1"
					}
					return "0"
				case *tag.Comm:
					return x.Text
				}
			}
		}
		return ""
	}
	t.Grouping = clean(str("TIT1", "TT1", "\xa9grp"))
	if m.Format() == tag.MP4 {
		// dhowden/tag only reads the first byte of 'tmpo'; read it ourselves.
		if v, ok := mp4ItemData(f, "tmpo"); ok && len(v) >= 2 {
			raw["tmpo"] = int(binary.BigEndian.Uint16(v))
		}
	}
	if bpm, err := strconv.Atoi(strings.TrimSpace(str("TBPM", "TBP", "tmpo"))); err == nil && bpm > 0 && bpm < 1000 {
		t.BPM = bpm
	}
	c := str("TCMP", "TCP", "cpil")
	t.Compilation = c == "1" || strings.EqualFold(c, "true")
	if m.Format() == tag.VORBIS {
		// Vorbis comments (FLAC, Ogg): dhowden/tag fills a missing composer
		// with the artist and doesn't read these fields, so read them directly.
		t.Composer = clean(str("composer"))
		t.AlbumArtist = clean(str("albumartist", "album artist", "album_artist"))
		t.Grouping = clean(str("grouping", "contentgroup"))
		if bpm, err := strconv.Atoi(strings.TrimSpace(str("bpm"))); err == nil && bpm > 0 && bpm < 1000 {
			t.BPM = bpm
		}
		c := str("compilation")
		t.Compilation = c == "1" || strings.EqualFold(c, "true")
		if n, err := strconv.Atoi(strings.TrimSpace(str("tracktotal", "totaltracks"))); err == nil && n > 0 && t.TrackTotal == 0 {
			t.TrackTotal = n
		}
		if n, err := strconv.Atoi(strings.TrimSpace(str("disctotal", "totaldiscs"))); err == nil && n > 0 && t.DiscTotal == 0 {
			t.DiscTotal = n
		}
	}
	if t.Comment != "" && (strings.HasPrefix(t.Comment, " 0000") || strings.HasPrefix(t.Comment, " 00000")) {
		t.Comment = "" // iTunNORM / iTunSMPB data mistaken for a comment
	}
	return t
}

func clean(s string) string {
	return strings.TrimSpace(strings.TrimRight(s, "\x00"))
}

// ------------------------------------------------------------------ MP3

var mp3Bitrates = [2][3][16]int{
	{ // MPEG-1
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{ // MPEG-2 / 2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}

var mp3Rates = [4][3]int{
	{11025, 12000, 8000},  // MPEG 2.5
	{0, 0, 0},             // reserved
	{22050, 24000, 16000}, // MPEG 2
	{44100, 48000, 32000}, // MPEG 1
}

type mp3Frame struct {
	version    int // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5
	layer      int // 1..3
	bitrate    int // kbps
	sampleRate int
	padding    int
	mono       bool
	length     int
	samples    int
}

func parseMP3Header(h []byte) (mp3Frame, bool) {
	var fr mp3Frame
	if len(h) < 4 || h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return fr, false
	}
	fr.version = int(h[1]>>3) & 3
	layerBits := int(h[1]>>1) & 3
	if fr.version == 1 || layerBits == 0 {
		return fr, false
	}
	fr.layer = 4 - layerBits
	bi := int(h[2] >> 4)
	si := int(h[2]>>2) & 3
	if bi == 0 || bi == 15 || si == 3 {
		return fr, false
	}
	vi := 0
	if fr.version != 3 {
		vi = 1
	}
	fr.bitrate = mp3Bitrates[vi][fr.layer-1][bi]
	fr.sampleRate = mp3Rates[fr.version][si]
	fr.padding = int(h[2]>>1) & 1
	fr.mono = h[3]>>6 == 3
	switch fr.layer {
	case 1:
		fr.samples = 384
		fr.length = (12*fr.bitrate*1000/fr.sampleRate + fr.padding) * 4
	case 2:
		fr.samples = 1152
		fr.length = 144*fr.bitrate*1000/fr.sampleRate + fr.padding
	case 3:
		if fr.version == 3 {
			fr.samples = 1152
			fr.length = 144*fr.bitrate*1000/fr.sampleRate + fr.padding
		} else {
			fr.samples = 576
			fr.length = 72*fr.bitrate*1000/fr.sampleRate + fr.padding
		}
	}
	return fr, fr.length > 4
}

// id3v2Size returns the total size of an ID3v2 tag at the start of b (0 if none).
func id3v2Size(b []byte) int {
	if len(b) < 10 || string(b[:3]) != "ID3" {
		return 0
	}
	sz := int(b[6]&0x7f)<<21 | int(b[7]&0x7f)<<14 | int(b[8]&0x7f)<<7 | int(b[9]&0x7f)
	sz += 10
	if b[5]&0x10 != 0 {
		sz += 10
	}
	return sz
}

func probeMP3(f *os.File, size int64, info *Info) error {
	start := int64(0)
	var hdr [10]byte
	for {
		if _, err := f.ReadAt(hdr[:], start); err != nil {
			return ErrUnsupported{"not a valid MP3 file"}
		}
		n := id3v2Size(hdr[:])
		if n == 0 {
			break
		}
		start += int64(n)
	}
	end := size
	var tail [128]byte
	if size >= 128 {
		if _, err := f.ReadAt(tail[:], size-128); err == nil && string(tail[:3]) == "TAG" {
			end -= 128
		}
	}
	// search for the first valid frame (confirmed by the following one)
	buf := make([]byte, 128*1024)
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	pos := -1
	var fr mp3Frame
	for i := 0; i+4 <= len(buf); i++ {
		h, ok := parseMP3Header(buf[i:])
		if !ok {
			continue
		}
		next := i + h.length
		if next+4 <= len(buf) {
			h2, ok2 := parseMP3Header(buf[next:])
			if !ok2 || h2.sampleRate != h.sampleRate || h2.layer != h.layer {
				continue
			}
		}
		pos, fr = i, h
		break
	}
	if pos < 0 {
		return ErrUnsupported{"no MPEG audio frames found"}
	}
	audioStart := start + int64(pos)
	info.Format = "mp3"
	info.FileTypeCode = "MP3 "
	info.Kind = "MPEG audio file"
	info.Ext = ".mp3"
	info.SampleRate = uint32(fr.sampleRate)
	info.Channels = 2
	if fr.mono {
		info.Channels = 1
	}

	// Xing / Info header
	frame := buf[pos:]
	sideInfo := 32
	if fr.version == 3 {
		if fr.mono {
			sideInfo = 17
		}
	} else {
		if fr.mono {
			sideInfo = 9
		} else {
			sideInfo = 17
		}
	}
	var frames, bytes uint32
	if x := 4 + sideInfo; x+16 <= len(frame) && (string(frame[x:x+4]) == "Xing" || string(frame[x:x+4]) == "Info") {
		flags := binary.BigEndian.Uint32(frame[x+4:])
		p := x + 8
		if flags&1 != 0 {
			frames = binary.BigEndian.Uint32(frame[p:])
			p += 4
		}
		if flags&2 != 0 && p+4 <= len(frame) {
			bytes = binary.BigEndian.Uint32(frame[p:])
		}
		info.VBR = string(frame[x:x+4]) == "Xing"
	} else if x := 4 + 32; x+18 <= len(frame) && string(frame[x:x+4]) == "VBRI" {
		bytes = binary.BigEndian.Uint32(frame[x+10:])
		frames = binary.BigEndian.Uint32(frame[x+14:])
		info.VBR = true
	}
	audioBytes := end - audioStart
	if frames > 0 {
		samples := uint64(frames) * uint64(fr.samples)
		info.SampleCount = samples
		info.LengthMs = uint32(samples * 1000 / uint64(fr.sampleRate))
		if bytes == 0 {
			bytes = uint32(audioBytes)
		}
		if info.LengthMs > 0 {
			info.Bitrate = uint32(uint64(bytes) * 8 / uint64(info.LengthMs))
		}
	} else {
		info.Bitrate = uint32(fr.bitrate)
		info.LengthMs = uint32(audioBytes * 8 / int64(fr.bitrate))
		info.SampleCount = uint64(info.LengthMs) * uint64(fr.sampleRate) / 1000
	}
	return nil
}

// ------------------------------------------------------------------ MP4

type atom struct {
	typ        string
	off        int64 // start of atom
	size       int64 // total size including header
	headerSize int64
}

func readAtoms(r io.ReaderAt, start, end int64) ([]atom, error) {
	var out []atom
	var h [16]byte
	for off := start; off+8 <= end; {
		if _, err := r.ReadAt(h[:8], off); err != nil {
			return out, err
		}
		size := int64(binary.BigEndian.Uint32(h[:4]))
		typ := string(h[4:8])
		hs := int64(8)
		if size == 1 {
			if _, err := r.ReadAt(h[8:16], off+8); err != nil {
				return out, err
			}
			size = int64(binary.BigEndian.Uint64(h[8:16]))
			hs = 16
		} else if size == 0 {
			size = end - off
		}
		if size < hs || off+size > end {
			return out, fmt.Errorf("corrupt MP4 atom %q", typ)
		}
		out = append(out, atom{typ, off, size, hs})
		off += size
	}
	return out, nil
}

func findAtom(list []atom, typ string) *atom {
	for i := range list {
		if list[i].typ == typ {
			return &list[i]
		}
	}
	return nil
}

func children(r io.ReaderAt, a *atom, skip int64) ([]atom, error) {
	return readAtoms(r, a.off+a.headerSize+skip, a.off+a.size)
}

func probeMP4(f *os.File, info *Info) error {
	st, _ := f.Stat()
	top, err := readAtoms(f, 0, st.Size())
	if err != nil && len(top) == 0 {
		return ErrUnsupported{"corrupt MP4 file"}
	}
	moov := findAtom(top, "moov")
	if moov == nil {
		return ErrUnsupported{"MP4 file has no movie header"}
	}
	mk, err := children(f, moov, 0)
	if err != nil {
		return ErrUnsupported{"corrupt MP4 file"}
	}
	var audioFound bool
	for i := range mk {
		if mk[i].typ != "trak" {
			continue
		}
		tk, _ := children(f, &mk[i], 0)
		mdia := findAtom(tk, "mdia")
		if mdia == nil {
			continue
		}
		md, _ := children(f, mdia, 0)
		hdlr := findAtom(md, "hdlr")
		if hdlr == nil {
			continue
		}
		var ht [4]byte
		f.ReadAt(ht[:], hdlr.off+hdlr.headerSize+8)
		switch string(ht[:]) {
		case "vide":
			return ErrUnsupported{"video files aren't supported – only songs"}
		case "soun":
		default:
			continue
		}
		if audioFound {
			continue
		}
		mdhd := findAtom(md, "mdhd")
		if mdhd != nil {
			b := make([]byte, 32)
			f.ReadAt(b, mdhd.off+mdhd.headerSize)
			var ts uint32
			var dur uint64
			if b[0] == 1 {
				ts = binary.BigEndian.Uint32(b[20:])
				dur = binary.BigEndian.Uint64(b[24:])
			} else {
				ts = binary.BigEndian.Uint32(b[12:])
				dur = uint64(binary.BigEndian.Uint32(b[16:]))
			}
			if ts > 0 {
				info.LengthMs = uint32(dur * 1000 / uint64(ts))
				info.SampleRate = ts
				info.SampleCount = dur
			}
		}
		minf := findAtom(md, "minf")
		if minf == nil {
			continue
		}
		mi, _ := children(f, minf, 0)
		stbl := findAtom(mi, "stbl")
		if stbl == nil {
			continue
		}
		sb, _ := children(f, stbl, 0)
		stsd := findAtom(sb, "stsd")
		if stsd == nil {
			continue
		}
		b := make([]byte, 48)
		f.ReadAt(b, stsd.off+stsd.headerSize)
		codec := string(b[12:16])
		sr := binary.BigEndian.Uint32(b[40:]) >> 16
		info.Channels = int(binary.BigEndian.Uint16(b[32:]))
		if codec == "alac" {
			info.Bits = int(binary.BigEndian.Uint16(b[34:]))
		}
		if sr > 0 && info.SampleRate == 0 {
			info.SampleRate = sr
		}
		if sr > 0 && sr != info.SampleRate && info.SampleRate%sr != 0 {
			// mdhd timescale isn't the sample rate (some encoders); rescale
			info.SampleCount = uint64(info.LengthMs) * uint64(sr) / 1000
			info.SampleRate = sr
		}
		switch codec {
		case "mp4a":
			info.Format, info.FileTypeCode, info.Kind = "aac", "M4A ", "AAC audio file"
		case "alac":
			info.Format, info.FileTypeCode, info.Kind = "alac", "M4A ", "Apple Lossless audio file"
		case "drms":
			info.Format, info.FileTypeCode, info.Kind = "aac", "M4P ", "Protected AAC audio file"
		default:
			return ErrUnsupported{"unsupported MP4 audio codec " + strings.TrimSpace(codec)}
		}
		info.Ext = ".m4a"
		if info.FileTypeCode == "M4P " {
			info.Ext = ".m4p"
		}
		audioFound = true
	}
	if !audioFound {
		return ErrUnsupported{"MP4 file has no audio track"}
	}
	if info.LengthMs == 0 {
		mvhd := findAtom(mk, "mvhd")
		if mvhd != nil {
			b := make([]byte, 32)
			f.ReadAt(b, mvhd.off+mvhd.headerSize)
			if b[0] == 0 {
				ts := binary.BigEndian.Uint32(b[12:])
				if ts > 0 {
					info.LengthMs = uint32(uint64(binary.BigEndian.Uint32(b[16:])) * 1000 / uint64(ts))
				}
			}
		}
	}
	if mdat := findAtom(top, "mdat"); mdat != nil && info.LengthMs > 0 {
		info.Bitrate = uint32(uint64(mdat.size) * 8 / uint64(info.LengthMs))
	}
	return nil
}

// ------------------------------------------------------------- WAV / AIFF

func probeWAV(f *os.File, info *Info) error {
	st, _ := f.Stat()
	var h [8]byte
	var byteRate, sr uint32
	var fmtTag uint16
	var dataSize int64
	for off := int64(12); off+8 <= st.Size(); {
		if _, err := f.ReadAt(h[:], off); err != nil {
			break
		}
		id := string(h[:4])
		sz := int64(binary.LittleEndian.Uint32(h[4:]))
		switch id {
		case "fmt ":
			b := make([]byte, 16)
			f.ReadAt(b, off+8)
			fmtTag = binary.LittleEndian.Uint16(b[0:])
			info.Channels = int(binary.LittleEndian.Uint16(b[2:]))
			sr = binary.LittleEndian.Uint32(b[4:])
			info.Bits = int(binary.LittleEndian.Uint16(b[14:]))
			byteRate = binary.LittleEndian.Uint32(b[8:])
		case "data":
			dataSize = sz
			if off+8+sz > st.Size() {
				dataSize = st.Size() - off - 8
			}
		}
		off += 8 + sz + sz%2
	}
	if fmtTag != 1 && fmtTag != 0xFFFE {
		return ErrUnsupported{"only uncompressed (PCM) WAV files are playable on an iPod"}
	}
	if byteRate == 0 || dataSize == 0 {
		return ErrUnsupported{"WAV file has no audio data"}
	}
	info.Format, info.FileTypeCode, info.Kind, info.Ext = "wav", "WAV ", "WAV audio file", ".wav"
	info.SampleRate = sr
	info.LengthMs = uint32(dataSize * 1000 / int64(byteRate))
	info.Bitrate = byteRate * 8 / 1000
	info.SampleCount = uint64(info.LengthMs) * uint64(sr) / 1000
	return nil
}

func probeAIFF(f *os.File, info *Info) error {
	st, _ := f.Stat()
	var h [8]byte
	found := false
	for off := int64(12); off+8 <= st.Size(); {
		if _, err := f.ReadAt(h[:], off); err != nil {
			break
		}
		id := string(h[:4])
		sz := int64(binary.BigEndian.Uint32(h[4:]))
		if id == "COMM" {
			b := make([]byte, 18)
			f.ReadAt(b, off+8)
			ch := binary.BigEndian.Uint16(b[0:])
			frames := binary.BigEndian.Uint32(b[2:])
			bits := binary.BigEndian.Uint16(b[6:])
			sr := extendedToFloat(b[8:18])
			if sr <= 0 {
				return ErrUnsupported{"invalid AIFF sample rate"}
			}
			info.SampleRate = uint32(sr)
			info.SampleCount = uint64(frames)
			info.LengthMs = uint32(float64(frames) * 1000 / sr)
			info.Bitrate = uint32(sr * float64(ch) * float64(bits) / 1000)
			info.Channels, info.Bits = int(ch), int(bits)
			found = true
		}
		off += 8 + sz + sz%2
	}
	if !found {
		return ErrUnsupported{"AIFF file has no COMM chunk"}
	}
	info.Format, info.FileTypeCode, info.Kind, info.Ext = "aiff", "AIFF", "AIFF audio file", ".aif"
	return nil
}

func extendedToFloat(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:])
	if exp == 0 && mant == 0 {
		return 0
	}
	return float64(mant) * math.Pow(2, float64(exp-16383-63))
}

var errNoTagSupport = errors.New("this file type doesn't support embedded tags")
