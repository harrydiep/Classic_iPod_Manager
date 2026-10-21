package media

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SourceInfo describes any audio file the Format Manager can read, whether
// or not an iPod can play it.
type SourceInfo struct {
	Format     string `json:"format"`     // mp3 aac alac wav aiff flac vorbis opus wma other
	Label      string `json:"label"`      // "MP3", "Apple Lossless", ...
	LengthMs   uint32 `json:"lengthMs"`   // 0 if unknown
	Bitrate    uint32 `json:"bitrate"`    // kbps, 0 if unknown
	SampleRate uint32 `json:"sampleRate"` // Hz, 0 if unknown
	Channels   int    `json:"channels"`
	Bits       int    `json:"bits"` // for PCM and lossless formats
	Size       int64  `json:"size"`
	Lossless   bool   `json:"lossless"`
	Playable   bool   `json:"playable"` // an iPod classic plays it as it is
	TagsInFile bool   `json:"tagsInFile"`
	Tags       Tags   `json:"tags"`
	HasArt     bool   `json:"hasArt"`
}

// FormatLabels names each format for people.
var FormatLabels = map[string]string{
	"mp3": "MP3", "aac": "AAC", "alac": "Apple Lossless", "wav": "WAV", "aiff": "AIFF",
	"flac": "FLAC", "vorbis": "Ogg Vorbis", "opus": "Opus", "wma": "WMA",
}

// DecodableExt reports whether a file looks like audio a decoder such as
// FFmpeg could read (a superset of what an iPod plays).
func DecodableExt(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp3", ".m4a", ".m4b", ".mp4", ".aac", ".wav", ".aif", ".aiff", ".aifc",
		".flac", ".ogg", ".oga", ".opus", ".wma", ".ape", ".wv", ".mpc", ".ac3", ".dts", ".mka", ".tta", ".alac", ".caf":
		return true
	}
	return false
}

// Inspect reads the format, technical details and tags of an audio file.
func Inspect(path string) (*SourceInfo, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("is a folder")
	}
	si := &SourceInfo{Size: st.Size()}
	info, perr := Probe(path)
	if perr == nil {
		si.Format = info.Format
		si.LengthMs, si.Bitrate, si.SampleRate = info.LengthMs, info.Bitrate, info.SampleRate
		si.Channels, si.Bits = info.Channels, info.Bits
		si.Tags = info.Tags
		si.Playable = info.FileTypeCode != "M4P "
		si.Lossless = info.Format == "alac" || info.Format == "wav" || info.Format == "aiff"
		si.TagsInFile = info.Format != "wav" && info.Format != "aiff"
		if info.FileTypeCode == "M4P " {
			si.Label = "Protected AAC"
		}
	} else {
		var unsupported ErrUnsupported
		if !errors.As(perr, &unsupported) {
			return nil, perr
		}
		if err := inspectOther(path, si); err != nil {
			return nil, err
		}
	}
	if si.Label == "" {
		si.Label = FormatLabels[si.Format]
	}
	if si.Label == "" {
		si.Label = strings.ToUpper(strings.TrimPrefix(filepath.Ext(path), "."))
	}
	if strings.TrimSpace(si.Tags.Title) == "" {
		base := filepath.Base(path)
		si.Tags.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if pic, _ := ReadArtwork(path); pic != nil {
		si.HasArt = true
	}
	return si, nil
}

func inspectOther(path string, si *SourceInfo) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var magic [16]byte
	n, _ := io.ReadFull(f, magic[:])
	switch {
	case n >= 4 && string(magic[:4]) == "fLaC":
		blocks, audioStart, err := readFLACBlocks(f)
		if err != nil {
			return err
		}
		rate, ch, bits, samples := flacStream(blocks[0].data)
		si.Format, si.Lossless, si.TagsInFile = "flac", true, true
		si.SampleRate, si.Channels, si.Bits = uint32(rate), ch, bits
		if rate > 0 {
			si.LengthMs = uint32(samples * 1000 / uint64(rate))
		}
		if si.LengthMs > 0 {
			si.Bitrate = uint32((si.Size - audioStart) * 8 / int64(si.LengthMs))
		}
	case n >= 4 && string(magic[:4]) == "OggS":
		codec, rate, ch, br, ms := oggInfo(f, si.Size)
		switch codec {
		case "vorbis", "opus":
			si.Format = codec
		case "flac":
			si.Format, si.Lossless = "flac", true
		default:
			si.Format = "other"
		}
		si.SampleRate, si.Channels, si.Bitrate, si.LengthMs = uint32(rate), ch, br, ms
		si.TagsInFile = true
	case n >= 4 && magic[0] == 0x30 && magic[1] == 0x26 && magic[2] == 0xB2 && magic[3] == 0x75:
		si.Format = "wma"
	default:
		if !DecodableExt(path) {
			return ErrUnsupported{"not an audio file"}
		}
		si.Format = "other"
	}
	f.Seek(0, io.SeekStart)
	si.Tags = readTags(f)
	return nil
}
