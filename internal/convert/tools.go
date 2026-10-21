// Package convert turns songs from one audio format into another using
// encoder programs the user puts into folders next to PodManager (LAME,
// qaac, FLAC, FFmpeg and so on). WAV and AIFF are written by PodManager
// itself.
package convert

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Format is an output format.
type Format struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Ext       string    `json:"ext"`
	IPod      bool      `json:"ipod"`     // an iPod classic plays it
	Lossless  bool      `json:"lossless"` // no quality is lost
	Builtin   bool      `json:"builtin"`  // written by PodManager, no encoder needed
	Tags      bool      `json:"tags"`     // the file can carry tags and artwork
	Folder    string    `json:"defaultFolder"`
	Qualities []Quality `json:"qualities"`
	Hint      string    `json:"hint"` // which programs work
}

// Quality is a bitrate or quality setting.
type Quality struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

func kbps(def int, list ...int) []Quality {
	var q []Quality
	for _, k := range list {
		q = append(q, Quality{ID: fmt.Sprintf("cbr%d", k), Label: fmt.Sprintf("%d kbps", k), Default: k == def})
	}
	return q
}

// Formats lists the output formats in menu order.
var Formats = []Format{
	{ID: "mp3", Label: "MP3", Ext: ".mp3", IPod: true, Tags: true, Folder: `Encoders\MP3`,
		Qualities: append(kbps(256, 128, 160, 192, 256, 320),
			Quality{ID: "v0", Label: "VBR V0 (about 245 kbps)"},
			Quality{ID: "v2", Label: "VBR V2 (about 190 kbps)"},
			Quality{ID: "v4", Label: "VBR V4 (about 165 kbps)"}),
		Hint: "lame.exe or ffmpeg.exe"},
	{ID: "aac", Label: "AAC", Ext: ".m4a", IPod: true, Tags: true, Folder: `Encoders\AAC`,
		Qualities: kbps(256, 96, 128, 160, 192, 256, 320),
		Hint:      "qaac64.exe, fdkaac.exe, neroAacEnc.exe or ffmpeg.exe"},
	{ID: "alac", Label: "Apple Lossless", Ext: ".m4a", IPod: true, Lossless: true, Tags: true, Folder: `Encoders\Apple Lossless`,
		Qualities: []Quality{{ID: "lossless", Label: "Lossless", Default: true}},
		Hint:      "refalac64.exe, qaac64.exe or ffmpeg.exe"},
	{ID: "wav", Label: "WAV", Ext: ".wav", IPod: true, Lossless: true, Builtin: true,
		Qualities: []Quality{{ID: "pcm", Label: "Uncompressed", Default: true}}},
	{ID: "aiff", Label: "AIFF", Ext: ".aif", IPod: true, Lossless: true, Builtin: true,
		Qualities: []Quality{{ID: "pcm", Label: "Uncompressed", Default: true}}},
	{ID: "flac", Label: "FLAC", Ext: ".flac", Lossless: true, Tags: true, Folder: `Encoders\FLAC`,
		Qualities: []Quality{{ID: "l5", Label: "Level 5 (faster)"}, {ID: "l8", Label: "Level 8 (smaller files)", Default: true}},
		Hint:      "flac.exe or ffmpeg.exe"},
	{ID: "vorbis", Label: "Ogg Vorbis", Ext: ".ogg", Tags: true, Folder: `Encoders\Ogg Vorbis`,
		Qualities: []Quality{{ID: "q3", Label: "Q3 (about 112 kbps)"}, {ID: "q5", Label: "Q5 (about 160 kbps)"},
			{ID: "q6", Label: "Q6 (about 192 kbps)", Default: true}, {ID: "q8", Label: "Q8 (about 256 kbps)"},
			{ID: "q10", Label: "Q10 (about 500 kbps)"}},
		Hint: "oggenc2.exe or ffmpeg.exe"},
}

// DecoderFolder is where a general decoder (FFmpeg) goes by default. It is
// used to read source formats PodManager can't read itself.
const DecoderFolder = `Encoders\Decoder`

// FormatByID returns a format.
func FormatByID(id string) (Format, bool) {
	for _, f := range Formats {
		if f.ID == id {
			return f, true
		}
	}
	return Format{}, false
}

// Tool kinds.
const (
	KindLame    = "lame"
	KindQaac    = "qaac"
	KindRefalac = "refalac"
	KindFdkaac  = "fdkaac"
	KindNero    = "nero"
	KindFlac    = "flac"
	KindOggenc  = "oggenc"
	KindOggdec  = "oggdec"
	KindFFmpeg  = "ffmpeg"
)

var toolNames = map[string]string{
	"lame": KindLame, "qaac64": KindQaac, "qaac": KindQaac, "refalac64": KindRefalac, "refalac": KindRefalac,
	"fdkaac": KindFdkaac, "neroaacenc": KindNero, "flac": KindFlac, "oggenc2": KindOggenc, "oggenc": KindOggenc,
	"oggdec": KindOggdec, "ffmpeg": KindFFmpeg,
}

// encoderKinds lists, per format, the programs that can write it, best first.
var encoderKinds = map[string][]string{
	"mp3":    {KindLame, KindFFmpeg},
	"aac":    {KindQaac, KindFdkaac, KindNero, KindFFmpeg},
	"alac":   {KindRefalac, KindQaac, KindFFmpeg},
	"flac":   {KindFlac, KindFFmpeg},
	"vorbis": {KindOggenc, KindFFmpeg},
}

// Tool is an encoder or decoder program found on disk.
type Tool struct {
	Name string `json:"name"` // file name, e.g. "lame.exe"
	Kind string `json:"kind"`
	Path string `json:"path"`
}

func toolKind(file string) string {
	base := strings.ToLower(file)
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(base, ".exe") {
			return ""
		}
		base = strings.TrimSuffix(base, ".exe")
	} else {
		base = strings.TrimSuffix(base, ".exe")
	}
	return toolNames[base]
}

// FindTools lists the known programs in dir and its immediate sub-folders
// (encoders are often unzipped into a folder of their own).
func FindTools(dir string) []Tool {
	var out []Tool
	seen := map[string]bool{}
	scan := func(d string, depth int) {}
	scan = func(d string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if e.IsDir() {
				if depth == 0 {
					scan(p, 1)
				}
				continue
			}
			if k := toolKind(e.Name()); k != "" && !seen[strings.ToLower(p)] {
				seen[strings.ToLower(p)] = true
				out = append(out, Tool{Name: e.Name(), Kind: k, Path: p})
			}
		}
	}
	scan(dir, 0)
	sort.Slice(out, func(i, j int) bool {
		if len(filepath.Dir(out[i].Path)) != len(filepath.Dir(out[j].Path)) {
			return len(filepath.Dir(out[i].Path)) < len(filepath.Dir(out[j].Path))
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// Config says where the encoders are.
type Config struct {
	Main    string            // PodManager's main folder
	Folders map[string]string // format ID or "decoder" → folder (relative to Main, or absolute)
	Choice  map[string]string // format ID → preferred program file name ("" = automatic)
}

// Dir returns the absolute encoder folder for a format ID or "decoder".
func (c Config) Dir(id string) string {
	f := c.Folders[id]
	if f == "" {
		if id == "decoder" {
			f = DecoderFolder
		} else if ff, ok := FormatByID(id); ok {
			f = ff.Folder
		}
	}
	if f == "" {
		return ""
	}
	f = filepath.FromSlash(strings.ReplaceAll(f, `\`, "/"))
	if filepath.IsAbs(f) || (len(f) > 1 && f[1] == ':') {
		return f
	}
	return filepath.Join(c.Main, f)
}

// ErrNoEncoder means no program for a format was found.
type ErrNoEncoder struct {
	Format Format
	Dir    string
}

func (e ErrNoEncoder) Error() string {
	return fmt.Sprintf("no %s encoder found – put %s in %s", e.Format.Label, e.Format.Hint, e.Dir)
}

// Encoder returns the program that writes a format.
func (c Config) Encoder(id string) (*Tool, error) {
	f, ok := FormatByID(id)
	if !ok {
		return nil, fmt.Errorf("unknown format %q", id)
	}
	if f.Builtin {
		return nil, nil
	}
	tools := FindTools(c.Dir(id))
	kinds := encoderKinds[id]
	usable := func(t Tool) bool {
		for _, k := range kinds {
			if t.Kind == k {
				return true
			}
		}
		return false
	}
	if want := c.Choice[id]; want != "" {
		for _, t := range tools {
			if strings.EqualFold(t.Name, want) && usable(t) {
				t := t
				return &t, nil
			}
		}
	}
	for _, k := range kinds {
		for _, t := range tools {
			if t.Kind == k {
				t := t
				return &t, nil
			}
		}
	}
	return nil, ErrNoEncoder{f, c.Dir(id)}
}

// ffmpeg returns an FFmpeg found in the decoder folder or any encoder folder.
func (c Config) ffmpeg() *Tool {
	ids := []string{"decoder"}
	for _, f := range Formats {
		if !f.Builtin {
			ids = append(ids, f.ID)
		}
	}
	for _, id := range ids {
		for _, t := range FindTools(c.Dir(id)) {
			if t.Kind == KindFFmpeg {
				t := t
				return &t
			}
		}
	}
	return nil
}

// decoderFor returns a program that can turn a source format into WAV.
func (c Config) decoderFor(srcFormat string) *Tool {
	if t := c.ffmpeg(); t != nil {
		return t
	}
	var folder string
	var kinds []string
	switch srcFormat {
	case "mp3":
		folder, kinds = "mp3", []string{KindLame}
	case "flac":
		folder, kinds = "flac", []string{KindFlac}
	case "alac":
		folder, kinds = "alac", []string{KindRefalac, KindQaac}
	case "aac":
		folder, kinds = "aac", []string{KindQaac}
	case "vorbis":
		folder, kinds = "vorbis", []string{KindOggdec}
	default:
		return nil
	}
	dirs := []string{c.Dir("decoder"), c.Dir(folder)}
	if srcFormat == "alac" {
		dirs = append(dirs, c.Dir("aac"))
	}
	if srcFormat == "aac" {
		dirs = append(dirs, c.Dir("alac"))
	}
	for _, k := range kinds {
		for _, d := range dirs {
			for _, t := range FindTools(d) {
				if t.Kind == k {
					t := t
					return &t
				}
			}
		}
	}
	return nil
}

// Status describes one row of the Options → Encoders section.
type Status struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Folder   string `json:"folder"` // as configured
	Path     string `json:"path"`   // absolute
	Exists   bool   `json:"exists"`
	Tools    []Tool `json:"tools"` // programs found that fit
	Choice   string `json:"choice"`
	Active   string `json:"active"` // program that will be used ("" = none)
	Builtin  bool   `json:"builtin"`
	Hint     string `json:"hint"`
	IPod     bool   `json:"ipod"`
	Lossless bool   `json:"lossless"`
}

// Statuses reports every format's encoder and the decoder.
func (c Config) Statuses() []Status {
	var out []Status
	for _, f := range Formats {
		s := Status{ID: f.ID, Label: f.Label, Builtin: f.Builtin, Hint: f.Hint, IPod: f.IPod, Lossless: f.Lossless, Choice: c.Choice[f.ID]}
		if f.Builtin {
			s.Active = "Built in"
			out = append(out, s)
			continue
		}
		s.Folder = c.Folders[f.ID]
		if s.Folder == "" {
			s.Folder = f.Folder
		}
		s.Path = c.Dir(f.ID)
		if st, err := os.Stat(s.Path); err == nil && st.IsDir() {
			s.Exists = true
		}
		for _, t := range FindTools(s.Path) {
			for _, k := range encoderKinds[f.ID] {
				if t.Kind == k {
					s.Tools = append(s.Tools, t)
				}
			}
		}
		if t, err := c.Encoder(f.ID); err == nil && t != nil {
			s.Active = t.Name
		}
		out = append(out, s)
	}
	d := Status{ID: "decoder", Label: "Decoder", Hint: "ffmpeg.exe (reads FLAC, Ogg, WMA and more)", Folder: c.Folders["decoder"]}
	if d.Folder == "" {
		d.Folder = DecoderFolder
	}
	d.Path = c.Dir("decoder")
	if st, err := os.Stat(d.Path); err == nil && st.IsDir() {
		d.Exists = true
	}
	for _, t := range FindTools(d.Path) {
		if t.Kind == KindFFmpeg {
			d.Tools = append(d.Tools, t)
		}
	}
	if t := c.ffmpeg(); t != nil {
		d.Active = t.Name
		if !strings.HasPrefix(strings.ToLower(t.Path), strings.ToLower(d.Path)) {
			d.Active = t.Name + " (from " + filepath.Base(filepath.Dir(t.Path)) + ")"
		}
	}
	return append(out, d)
}

// MakeFolders creates the encoder folders with a short read-me in each.
func (c Config) MakeFolders() error {
	if c.Main == "" {
		return errors.New("no main folder")
	}
	ids := []string{"decoder"}
	for _, f := range Formats {
		if !f.Builtin {
			ids = append(ids, f.ID)
		}
	}
	for _, id := range ids {
		dir := c.Dir(id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		hint := "ffmpeg.exe"
		label := "the decoder"
		if f, ok := FormatByID(id); ok {
			hint, label = f.Hint, f.Label
		}
		readme := filepath.Join(dir, "Put the encoder here.txt")
		if _, err := os.Stat(readme); errors.Is(err, os.ErrNotExist) {
			text := fmt.Sprintf("Put %s for %s in this folder (or in a folder inside it).\r\n"+
				"PodManager finds it automatically. You can choose a different folder in Options > Encoders.\r\n", hint, label)
			if id == "decoder" {
				text = "Put ffmpeg.exe in this folder (or in a folder inside it).\r\n" +
					"PodManager uses it to read formats it can't read itself (FLAC, Ogg Vorbis, Opus, WMA and others)\r\n" +
					"and to change sample rates. An ffmpeg.exe in any encoder folder works too.\r\n"
			}
			os.WriteFile(readme, []byte(text), 0o644)
		}
	}
	return nil
}
