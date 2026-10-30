package ipod

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Every iPod that shows up as a drive keeps its music in iPod_Control with
// an iTunesDB. What differs between generations is handled here:
//
//   - which album-art sizes the iPod uses (none on the monochrome iPods,
//     minis and shuffles),
//   - whether it plays video,
//   - whether its database is signed: the iPod classic and the 3rd/4th
//     generation nano need a "hash58" checksum made from their FireWire ID
//     (see hash58.go); the 5th generation nano and later use schemes only
//     iTunes can produce, so those are opened read-only,
//   - the shuffles, which play from a separate iTunesSD file (shuffle.go).

type generation int

const (
	genUnknown generation = iota
	genFirst
	genSecond
	genThird
	genFourth
	genPhoto
	genMobile
	genMini1
	genMini2
	genShuffle1
	genShuffle2
	genShuffle3
	genShuffle4
	genNano1
	genNano2
	genNano3
	genNano4
	genNano5
	genNano6
	genNano7
	genVideo1
	genVideo2
	genClassic1
	genClassic2
	genClassic3
	genTouch1
	genTouch2
	genTouch3
	genTouch4
	genIphone1
	genIphone2
	genIphone3
	genIphone4
	genIpad1
)

// Checksum is how an iPod signs its database.
type Checksum int

const (
	ChecksumNone   Checksum = iota
	ChecksumHash58          // iPod classic, nano 3G/4G: Classic iPod Manager can write it
	ChecksumHash72          // nano 5G: needs iTunes
	ChecksumHashAB          // nano 6G/7G: needs iTunes
)

// ArtFormat is one album-art thumbnail size the iPod uses.
type ArtFormat struct {
	ID   uint32
	Side int // square, RGB565 little-endian
}

var (
	artPhoto   = []ArtFormat{{1016, 140}, {1017, 56}}
	artNano    = []ArtFormat{{1027, 100}, {1031, 42}}
	artVideo   = []ArtFormat{{1029, 200}, {1028, 100}}
	artClassic = []ArtFormat{{1060, 320}, {1055, 128}, {1068, 128}, {1061, 56}}
	artNano4   = []ArtFormat{{1071, 240}, {1084, 240}, {1055, 128}, {1068, 128}, {1078, 80}, {1074, 50}}
)

type genInfo struct {
	family   string // "iPod", "iPod mini", "iPod nano", "iPod shuffle", "iPod classic", "iPod photo"
	label    string // "5th generation"
	video    bool
	art      []ArtFormat
	checksum Checksum
	shuffle  int // iTunesSD format: 1 (shuffle 1G/2G) or 2 (3G/4G); 0 = not a shuffle
	noDisk   bool
}

var gens = map[generation]genInfo{
	genUnknown:  {family: "iPod"},
	genFirst:    {family: "iPod", label: "1st generation"},
	genSecond:   {family: "iPod", label: "2nd generation"},
	genThird:    {family: "iPod", label: "3rd generation"},
	genFourth:   {family: "iPod", label: "4th generation"},
	genPhoto:    {family: "iPod photo", label: "4th generation (colour display)", art: artPhoto},
	genMobile:   {family: "iTunes phone", label: "Motorola ROKR"},
	genMini1:    {family: "iPod mini", label: "1st generation"},
	genMini2:    {family: "iPod mini", label: "2nd generation"},
	genShuffle1: {family: "iPod shuffle", label: "1st generation", shuffle: 1},
	genShuffle2: {family: "iPod shuffle", label: "2nd generation", shuffle: 1},
	genShuffle3: {family: "iPod shuffle", label: "3rd generation", shuffle: 2},
	genShuffle4: {family: "iPod shuffle", label: "4th generation", shuffle: 2},
	genNano1:    {family: "iPod nano", label: "1st generation", art: artNano},
	genNano2:    {family: "iPod nano", label: "2nd generation", art: artNano},
	genNano3:    {family: "iPod nano", label: "3rd generation", video: true, art: artClassic, checksum: ChecksumHash58},
	genNano4:    {family: "iPod nano", label: "4th generation", video: true, art: artNano4, checksum: ChecksumHash58},
	genNano5:    {family: "iPod nano", label: "5th generation", video: true, checksum: ChecksumHash72},
	genNano6:    {family: "iPod nano", label: "6th generation", checksum: ChecksumHashAB},
	genNano7:    {family: "iPod nano", label: "7th generation", video: true, checksum: ChecksumHashAB},
	genVideo1:   {family: "iPod", label: "5th generation", video: true, art: artVideo},
	genVideo2:   {family: "iPod", label: "5th generation (late 2006)", video: true, art: artVideo},
	genClassic1: {family: "iPod classic", label: "6th generation", video: true, art: artClassic, checksum: ChecksumHash58},
	genClassic2: {family: "iPod classic", label: "6th generation (2008)", video: true, art: artClassic, checksum: ChecksumHash58},
	genClassic3: {family: "iPod classic", label: "7th generation", video: true, art: artClassic, checksum: ChecksumHash58},
	genTouch1:   {family: "iPod touch", label: "1st generation", noDisk: true},
	genTouch2:   {family: "iPod touch", label: "2nd generation", noDisk: true},
	genTouch3:   {family: "iPod touch", label: "3rd generation", noDisk: true},
	genTouch4:   {family: "iPod touch", label: "4th generation", noDisk: true},
	genIphone1:  {family: "iPhone", noDisk: true},
	genIphone2:  {family: "iPhone 3G", noDisk: true},
	genIphone3:  {family: "iPhone 3GS", noDisk: true},
	genIphone4:  {family: "iPhone 4", noDisk: true},
	genIpad1:    {family: "iPad", noDisk: true},
}

type modelRow struct {
	capacity float64
	color    string
	gen      generation
	dirs     int
}

// nano 7th generation (2012), which libgpod's table predates
func init() {
	for num, c := range map[string]string{"D475": "pink", "D476": "yellow", "D477": "blue", "D478": "green",
		"D479": "purple", "D480": "silver", "D481": "black", "D744": "red"} {
		modelTable[num] = modelRow{16, c, genNano7, 14}
	}
}

// Model describes a recognised iPod and what Classic iPod Manager can do
// with it.
type Model struct {
	Number     string  `json:"number"`
	Name       string  `json:"name"`
	Generation string  `json:"generation"`
	CapacityGB float64 `json:"capacityGB"`
	Color      string  `json:"color"` // white, black, silver, u2, blue, pink…
	Family     string  `json:"family"`
	Video      bool    `json:"video"`   // plays video
	Artwork    bool    `json:"artwork"` // shows album art
	Shuffle    bool    `json:"shuffle"` // no screen; plays from iTunesSD
	// Supported is false for iPods whose database Classic iPod Manager
	// can't write (it opens them read-only).
	Supported bool `json:"supported"`

	gen       generation
	known     bool
	art       []ArtFormat
	checksum  Checksum
	shuffleDB int
	musicDirs int
}

func modelFrom(num string, row modelRow) Model {
	g := gens[row.gen]
	name := g.family
	if row.capacity > 0 {
		if row.capacity < 1 {
			name += fmt.Sprintf(" %dMB", int(row.capacity*1024+0.5))
		} else {
			name += fmt.Sprintf(" %gGB", row.capacity)
		}
	}
	if row.color == "u2" {
		name = strings.Replace(name, g.family, g.family+" U2 Special Edition", 1)
	}
	m := Model{Number: num, Name: name, Generation: g.label, CapacityGB: row.capacity, Color: row.color,
		Family: g.family, Video: g.video, Artwork: len(g.art) > 0, Shuffle: g.shuffle > 0,
		gen: row.gen, known: true, art: g.art, checksum: g.checksum, shuffleDB: g.shuffle, musicDirs: row.dirs}
	m.Supported = !g.noDisk && (m.checksum == ChecksumNone || m.checksum == ChecksumHash58)
	return m
}

func unknownModel(label string) Model {
	return Model{Name: label, Family: "iPod", Supported: true, Video: true, musicDirs: 20}
}

// ReadModel works out which iPod is at root from iPod_Control/Device
// (SysInfo, written by the iPod; SysInfoExtended, written by iTunes or
// tools) and returns it with the firmware version.
func ReadModel(root string) (Model, string) {
	dev := filepath.Join(root, "iPod_Control", "Device")
	si := readSysInfo(filepath.Join(dev, "SysInfo"))
	ext, _ := os.ReadFile(filepath.Join(dev, "SysInfoExtended"))
	fw := ""
	if v := si["visibleBuildID"]; v != "" {
		if i := strings.Index(v, "("); i >= 0 {
			fw = strings.Trim(v[i:], "()")
		}
	}
	if fw == "" {
		fw = plistString(ext, "VisibleBuildID")
	}
	// the model number: "xA147" or "MA147LL" → "A147"
	var m Model
	num := si["ModelNumStr"]
	if num == "" {
		num = plistString(ext, "ModelNumStr")
	}
	if num != "" {
		n := strings.TrimLeft(num, "xXMm")
		for _, k := range []string{n, strings.TrimRightFunc(n, func(r rune) bool { return r >= 'A' && r <= 'Z' })} {
			if len(k) > 4 {
				k = k[:4]
			}
			if row, ok := modelTable[k]; ok {
				m = modelFrom(k, row)
				break
			}
		}
	}
	// or the last three characters of the serial number
	if !m.known {
		serial := si["pszSerialNumber"]
		if serial == "" {
			serial = plistString(ext, "SerialNumber")
		}
		if len(serial) >= 3 {
			if k, ok := serialModels[strings.ToUpper(serial[len(serial)-3:])]; ok {
				m = modelFrom(k, modelTable[k])
			}
		}
	}
	if !m.known {
		m = unknownModel("iPod")
		if num != "" {
			m.Name = "iPod (" + num + ")"
			m.Number = num
		}
	}
	// SysInfoExtended says which signature the database needs; trust it
	// over the model table.
	if v := plistInt(ext, "DBVersion"); v > 0 {
		switch {
		case v <= 2:
			m.checksum = ChecksumNone
		case v == 3:
			m.checksum = ChecksumHash58
		case v == 4:
			m.checksum = ChecksumHash72
		default:
			m.checksum = ChecksumHashAB
		}
		if m.known {
			m.Supported = !gens[m.gen].noDisk && (m.checksum == ChecksumNone || m.checksum == ChecksumHash58)
		}
	}
	return m, fw
}

func readSysInfo(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func plistString(b []byte, key string) string {
	if len(b) == 0 {
		return ""
	}
	re := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<string>([^<]*)</string>`)
	if m := re.FindSubmatch(b); m != nil {
		return strings.TrimSpace(string(m[1]))
	}
	return ""
}

func plistInt(b []byte, key string) int {
	if len(b) == 0 {
		return 0
	}
	re := regexp.MustCompile(`<key>` + regexp.QuoteMeta(key) + `</key>\s*<integer>(\d+)</integer>`)
	if m := re.FindSubmatch(b); m != nil {
		n := 0
		fmt.Sscan(string(m[1]), &n)
		return n
	}
	return 0
}

// dbChecksum reads which signature an existing database carries
// (mhbd+0x30), for iPods the model table doesn't know.
func dbChecksum(db []byte) Checksum {
	if len(db) < 0x32 {
		return ChecksumNone
	}
	hl := int(binary.LittleEndian.Uint32(db[4:]))
	if hl < 0x32 || hl > len(db) {
		return ChecksumNone
	}
	switch binary.LittleEndian.Uint16(db[0x30:]) {
	case 1:
		return ChecksumHash58
	case 2:
		return ChecksumHash72
	case 0:
		if hl >= 0x6C {
			for _, b := range db[0x58:0x6C] {
				if b != 0 {
					return ChecksumHash58
				}
			}
		}
		if hl >= 0x72 && binary.LittleEndian.Uint16(db[0x70:]) != 0 {
			return ChecksumHash72
		}
		return ChecksumNone
	}
	return ChecksumHashAB
}

// PlayProblem says why the iPod can't play songs of a format, if it can't ("mp3", "aac",
// "alac", "wav", "aiff"): Apple Lossless arrived with the 3rd generation
// iPod and the 3rd generation shuffle, AIFF with the 2nd generation shuffle.
func (m Model) PlayProblem(format string) string {
	switch m.gen {
	case genFirst, genSecond, genShuffle1, genShuffle2:
		if format == "alac" {
			return "this iPod doesn't play Apple Lossless – convert it to AAC or MP3 in the Format Manager"
		}
	}
	if m.gen == genShuffle1 && format == "aiff" {
		return "this iPod doesn't play AIFF – convert it to AAC or MP3 in the Format Manager"
	}
	return ""
}

// Checksum reports how the iPod's database is signed.
func (m Model) ChecksumKind() Checksum { return m.checksum }

// ArtFormats lists the album-art sizes the iPod uses.
func (m Model) ArtFormats() []ArtFormat { return m.art }

func (c Checksum) String() string {
	switch c {
	case ChecksumHash58:
		return "hash58"
	case ChecksumHash72:
		return "hash72"
	case ChecksumHashAB:
		return "hashAB"
	}
	return "none"
}
