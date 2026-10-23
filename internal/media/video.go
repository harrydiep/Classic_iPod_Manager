package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Videos for the iPod 5th generation: MPEG-4 files (.m4v/.mp4) with
//
//   - H.264 Baseline profile, level 3.0 or lower, up to 640×480 and about
//     1.5 Mbit/s, or
//   - MPEG-4 Part 2 Simple profile up to 480×480 (640×480 on later
//     firmware) and about 2.5 Mbit/s,
//
// with AAC sound. Anything else has to be converted first.

// VideoInfo describes a video file.
type VideoInfo struct {
	Container  string // "mp4", "mov" or "other"
	VideoCodec string // "h264", "mpeg4", or the codec's four letters
	Profile    int    // H.264 profile_idc (66 = Baseline)
	Level      int    // H.264 level_idc (30 = 3.0)
	Width      int
	Height     int
	AudioCodec string // "aac", "" (no sound) or the codec's four letters
	LengthMs   uint32
	Bitrate    uint32 // kbps, whole file
	SampleRate uint32 // of the sound
	Size       int64
	Tags       Tags
	Playable   bool   // an iPod plays it as it is
	Reason     string // why not
	Remux      bool   // the streams are fine; only the container must change
}

var videoExts = map[string]bool{".m4v": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true, ".wmv": true,
	".webm": true, ".flv": true, ".mpg": true, ".mpeg": true, ".m2ts": true, ".mts": true, ".ts": true, ".3gp": true,
	".divx": true, ".vob": true, ".ogv": true, ".asf": true}

// VideoExt reports whether a file name looks like a video. (.mp4 may also
// be a song; IsVideo looks inside.)
func VideoExt(path string) bool { return videoExts[strings.ToLower(filepath.Ext(path))] }

// IsVideo reports whether a file is a video: a video extension, and for
// MPEG-4 files, a video track inside.
func IsVideo(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if !videoExts[ext] {
		return false
	}
	if ext != ".mp4" && ext != ".m4v" && ext != ".mov" && ext != ".3gp" {
		return true
	}
	vi, err := ProbeVideo(path)
	return err == nil && vi.VideoCodec != ""
}

// ProbeVideo reads a video file's codecs, size and tags, and decides whether
// an iPod can play it.
func ProbeVideo(path string) (*VideoInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	vi := &VideoInfo{Size: st.Size(), Container: "other"}
	var head [12]byte
	io.ReadFull(f, head[:])
	isMP4 := string(head[4:8]) == "ftyp" || string(head[4:8]) == "moov" || string(head[4:8]) == "mdat" || string(head[4:8]) == "wide"
	if isMP4 {
		vi.Container = "mp4"
		brand := string(head[8:12])
		if brand == "qt  " || strings.EqualFold(filepath.Ext(path), ".mov") {
			vi.Container = "mov"
		}
		if err := probeVideoMP4(f, vi); err != nil {
			return nil, err
		}
		vi.Tags = readTags(f)
	}
	if strings.TrimSpace(vi.Tags.Title) == "" {
		base := filepath.Base(path)
		vi.Tags.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if vi.Bitrate == 0 && vi.LengthMs > 0 {
		vi.Bitrate = uint32(vi.Size * 8 / int64(vi.LengthMs))
	}
	vi.judge()
	return vi, nil
}

func (vi *VideoInfo) judge() {
	reasons := []string{}
	streamsOK := true
	switch vi.VideoCodec {
	case "h264":
		if vi.Profile != 66 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("H.264 %s profile (the iPod needs Baseline)", h264Profile(vi.Profile)))
		}
		if vi.Level > 30 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("H.264 level %d.%d (the iPod needs 3.0 or lower)", vi.Level/10, vi.Level%10))
		}
		if vi.Width > 640 || vi.Height > 480 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("%d×%d picture (the iPod plays up to 640×480)", vi.Width, vi.Height))
		}
		if vi.Bitrate > 1800 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("%d kbps (the iPod plays up to about 1.5 Mbps H.264)", vi.Bitrate))
		}
	case "mpeg4":
		if vi.Width > 640 || vi.Height > 480 || vi.Width*vi.Height > 640*480 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("%d×%d picture (too large for the iPod)", vi.Width, vi.Height))
		}
		if vi.Bitrate > 2700 {
			streamsOK = false
			reasons = append(reasons, fmt.Sprintf("%d kbps (the iPod plays up to 2.5 Mbps MPEG-4)", vi.Bitrate))
		}
	case "":
		streamsOK = false
		if vi.Container == "other" {
			reasons = append(reasons, "this kind of video file needs converting for the iPod")
		} else {
			reasons = append(reasons, "no video found in the file")
		}
	default:
		streamsOK = false
		reasons = append(reasons, "the "+strings.TrimSpace(vi.VideoCodec)+" video codec isn't supported by the iPod")
	}
	if vi.AudioCodec != "" && vi.AudioCodec != "aac" {
		streamsOK = false
		reasons = append(reasons, "the sound isn't AAC")
	}
	switch {
	case streamsOK && vi.Container == "mp4":
		vi.Playable = true
	case streamsOK && vi.Container == "mov":
		vi.Remux = true
		reasons = append(reasons, "QuickTime .mov files need re-packaging as .m4v")
	}
	vi.Reason = strings.Join(reasons, "; ")
}

func h264Profile(p int) string {
	switch p {
	case 66:
		return "Baseline"
	case 77:
		return "Main"
	case 88:
		return "Extended"
	case 100:
		return "High"
	case 110:
		return "High 10"
	case 122:
		return "High 4:2:2"
	}
	return fmt.Sprintf("%d", p)
}

func probeVideoMP4(f *os.File, vi *VideoInfo) error {
	top, err := readAtoms(f, 0, vi.Size)
	if err != nil && len(top) == 0 {
		return ErrUnsupported{"corrupt video file"}
	}
	moov := findAtom(top, "moov")
	if moov == nil {
		return ErrUnsupported{"the video file has no movie header"}
	}
	mk, err := children(f, moov, 0)
	if err != nil {
		return ErrUnsupported{"corrupt video file"}
	}
	if mvhd := findAtom(mk, "mvhd"); mvhd != nil {
		b := make([]byte, 32)
		f.ReadAt(b, mvhd.off+mvhd.headerSize)
		if b[0] == 1 {
			ts := binary.BigEndian.Uint32(b[20:])
			var d [8]byte
			f.ReadAt(d[:], mvhd.off+mvhd.headerSize+24)
			if ts > 0 {
				vi.LengthMs = uint32(binary.BigEndian.Uint64(d[:]) * 1000 / uint64(ts))
			}
		} else if ts := binary.BigEndian.Uint32(b[12:]); ts > 0 {
			vi.LengthMs = uint32(uint64(binary.BigEndian.Uint32(b[16:])) * 1000 / uint64(ts))
		}
	}
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
		minf := findAtom(md, "minf")
		if hdlr == nil || minf == nil {
			continue
		}
		var ht [4]byte
		f.ReadAt(ht[:], hdlr.off+hdlr.headerSize+8)
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
		n := stsd.size - stsd.headerSize
		if n > 4096 {
			n = 4096
		}
		b := make([]byte, n)
		f.ReadAt(b, stsd.off+stsd.headerSize)
		if len(b) < 16 {
			continue
		}
		codec := string(b[12:16])
		switch string(ht[:]) {
		case "vide":
			if vi.VideoCodec != "" {
				continue
			}
			switch codec {
			case "avc1", "avc3":
				vi.VideoCodec = "h264"
			case "mp4v":
				vi.VideoCodec = "mpeg4"
			default:
				vi.VideoCodec = codec
			}
			if len(b) >= 44 {
				vi.Width = int(binary.BigEndian.Uint16(b[40:]))
				vi.Height = int(binary.BigEndian.Uint16(b[42:]))
			}
			// avcC sits among the sample entry's child boxes (after 86 bytes)
			if vi.VideoCodec == "h264" {
				entry := b[8:]
				if i := bytes.Index(entry, []byte("avcC")); i >= 4 && i+8 <= len(entry) {
					vi.Profile = int(entry[i+5])
					vi.Level = int(entry[i+7])
				}
			}
		case "soun":
			if vi.AudioCodec != "" {
				continue
			}
			switch {
			case codec == "mp4a" && mp4aIsAAC(b):
				vi.AudioCodec = "aac"
			case codec == "mp4a":
				vi.AudioCodec = "mp4a (not AAC)"
			default:
				vi.AudioCodec = codec
			}
			if len(b) >= 44 {
				vi.SampleRate = binary.BigEndian.Uint32(b[40:]) >> 16
			}
		}
	}
	return nil
}

// mp4aIsAAC reads the object type in an mp4a entry's esds box: 0x40 and
// 0x66-0x68 are AAC; 0x69/0x6B are MP3 inside MP4.
func mp4aIsAAC(stsd []byte) bool {
	i := bytes.Index(stsd, []byte("esds"))
	if i < 0 || i+9 > len(stsd) {
		return true // no esds: assume AAC, as players do
	}
	d := stsd[i+8:] // after version/flags
	readLen := func(p int) (int, int) {
		n := 0
		for k := 0; k < 4 && p < len(d); k++ {
			c := d[p]
			p++
			n = n<<7 | int(c&0x7F)
			if c&0x80 == 0 {
				break
			}
		}
		return n, p
	}
	p := 0
	if p >= len(d) || d[p] != 0x03 {
		return true
	}
	_, p = readLen(p + 1)
	if p+3 > len(d) {
		return true
	}
	flags := d[p+2]
	p += 3
	if flags&0x80 != 0 {
		p += 2
	}
	if flags&0x40 != 0 && p < len(d) {
		p += 1 + int(d[p])
	}
	if flags&0x20 != 0 {
		p += 2
	}
	if p >= len(d) || d[p] != 0x04 {
		return true
	}
	_, p = readLen(p + 1)
	if p >= len(d) {
		return true
	}
	ot := d[p]
	return ot == 0x40 || (ot >= 0x66 && ot <= 0x68)
}
