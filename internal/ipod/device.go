// Package ipod handles an iPod's file system layout: finding the database,
// loading and saving it safely, and allocating file names for new songs.
package ipod

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"podmanager/internal/itdb"
)

// Model describes a recognised iPod.
type Model struct {
	Number     string `json:"number"`
	Name       string `json:"name"`
	Generation string `json:"generation"`
	CapacityGB int    `json:"capacityGB"`
	Color      string `json:"color"` // white, black, u2, silver
	Supported  bool   `json:"supported"`
}

var models = map[string]Model{
	"A002": {"A002", "iPod 30GB", "5th generation", 30, "white", true},
	"A146": {"A146", "iPod 30GB", "5th generation", 30, "black", true},
	"A003": {"A003", "iPod 60GB", "5th generation", 60, "white", true},
	"A147": {"A147", "iPod 60GB", "5th generation", 60, "black", true},
	"A452": {"A452", "iPod U2 Special Edition 30GB", "5th generation", 30, "u2", true},
	"A444": {"A444", "iPod 30GB", "5th generation (late 2006)", 30, "white", true},
	"A446": {"A446", "iPod 30GB", "5th generation (late 2006)", 30, "black", true},
	"A664": {"A664", "iPod U2 Special Edition 30GB", "5th generation (late 2006)", 30, "u2", true},
	"A448": {"A448", "iPod 80GB", "5th generation (late 2006)", 80, "white", true},
	"A450": {"A450", "iPod 80GB", "5th generation (late 2006)", 80, "black", true},
	"B029": {"B029", "iPod classic 80GB", "6th generation", 80, "silver", false},
	"B147": {"B147", "iPod classic 80GB", "6th generation", 80, "black", false},
	"B145": {"B145", "iPod classic 160GB", "6th generation", 160, "silver", false},
	"B150": {"B150", "iPod classic 160GB", "6th generation", 160, "black", false},
	"B562": {"B562", "iPod classic 120GB", "6th generation (2008)", 120, "silver", false},
	"B565": {"B565", "iPod classic 120GB", "6th generation (2008)", 120, "black", false},
	"C293": {"C293", "iPod classic 160GB", "7th generation", 160, "silver", false},
	"C297": {"C297", "iPod classic 160GB", "7th generation", 160, "black", false},
}

// Device is an opened iPod.
type Device struct {
	Root     string
	DB       *itdb.DB
	Model    Model
	Firmware string
	ReadOnly string // non-empty: reason the database must not be written

	backupDir string
	backedUp  bool
	dirty     bool // Play Counts/OTG merged, needs cleaning on save

	artOnce sync.Once
	art     map[uint64][]thumbRef

	artw        *artDB // ArtworkDB loaded for writing
	artDirty    bool
	artBackedUp bool
}

// ErrNoDatabase is returned when an iPod has no iTunesDB yet.
var ErrNoDatabase = errors.New("this iPod has no music database yet")

func (d *Device) itunesDir() string { return filepath.Join(d.Root, "iPod_Control", "iTunes") }
func (d *Device) dbPath() string    { return filepath.Join(d.itunesDir(), "iTunesDB") }

// LooksLikeIPod reports whether root contains an iPod_Control folder.
func LooksLikeIPod(root string) bool {
	st, err := os.Stat(filepath.Join(root, "iPod_Control"))
	return err == nil && st.IsDir()
}

// HasDatabase reports whether root has an iTunesDB.
func HasDatabase(root string) bool {
	_, err := os.Stat(filepath.Join(root, "iPod_Control", "iTunes", "iTunesDB"))
	return err == nil
}

// ReadModel reads iPod_Control/Device/SysInfo.
func ReadModel(root string) (Model, string) {
	var m Model
	fw := ""
	f, err := os.Open(filepath.Join(root, "iPod_Control", "Device", "SysInfo"))
	if err != nil {
		return Model{Name: "iPod", Supported: true}, ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "ModelNumStr":
			if len(v) >= 4 {
				num := v[len(v)-4:]
				if mm, ok := models[num]; ok {
					m = mm
				} else {
					m = Model{Number: num, Name: "iPod (" + v + ")", Supported: true}
				}
			}
		case "visibleBuildID":
			if i := strings.Index(v, "("); i >= 0 {
				fw = strings.Trim(v[i:], "()")
			}
		}
	}
	if m.Name == "" {
		m = Model{Name: "iPod", Supported: true}
	}
	return m, fw
}

// Open loads the iPod's database. backupDir is where database backups are
// kept on the computer.
func Open(root, backupDir string) (*Device, error) {
	d := &Device{Root: root, backupDir: backupDir}
	d.Model, d.Firmware = ReadModel(root)
	data, err := os.ReadFile(d.dbPath())
	if errors.Is(err, os.ErrNotExist) {
		return d, ErrNoDatabase
	}
	if err != nil {
		return nil, err
	}
	db, err := itdb.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("could not read the iPod's music database: %w", err)
	}
	d.DB = db
	if !d.Model.Supported {
		d.ReadOnly = d.Model.Name + " (" + d.Model.Generation + ") protects its database with a checksum, so Classic iPod Manager can only view it."
	} else if hasChecksum(data) {
		d.ReadOnly = "This iPod's database is protected with a checksum (newer iPod), so Classic iPod Manager can only view it."
	}
	if pc, err := os.ReadFile(filepath.Join(d.itunesDir(), "Play Counts")); err == nil {
		if db.MergePlayCounts(pc) == nil {
			d.dirty = true
		}
	}
	otgs, _ := filepath.Glob(filepath.Join(d.itunesDir(), "OTGPlaylistInfo*"))
	sort.Strings(otgs)
	n := 1
	for _, p := range otgs {
		if b, err := os.ReadFile(p); err == nil {
			if ok, _ := db.ImportOTG(b, fmt.Sprintf("On-The-Go %d", n)); ok {
				n++
			}
			d.dirty = true
		}
	}
	return d, nil
}

func hasChecksum(db []byte) bool {
	if len(db) < 12 {
		return false
	}
	hl := int(binary.LittleEndian.Uint32(db[4:]))
	if hl > len(db) {
		return false
	}
	if hl >= 0x32 && binary.LittleEndian.Uint16(db[0x30:]) == 1 { // hash58 scheme marker
		return true
	}
	if hl >= 0x6C {
		for _, b := range db[0x58:0x6C] { // hash58 bytes
			if b != 0 {
				return true
			}
		}
	}
	if hl >= 0x72 && binary.LittleEndian.Uint16(db[0x70:]) != 0 { // hash72 / hashAB scheme
		return true
	}
	return false
}

// Initialize creates the folder structure and an empty database.
func Initialize(root, name, backupDir string) (*Device, error) {
	for i := 0; i < 50; i++ {
		if err := os.MkdirAll(filepath.Join(root, "iPod_Control", "Music", fmt.Sprintf("F%02d", i)), 0o755); err != nil {
			return nil, err
		}
	}
	for _, sub := range []string{"iTunes", "Device", "Artwork"} {
		if err := os.MkdirAll(filepath.Join(root, "iPod_Control", sub), 0o755); err != nil {
			return nil, err
		}
	}
	d := &Device{Root: root, backupDir: backupDir, DB: itdb.New(name)}
	d.Model, d.Firmware = ReadModel(root)
	return d, d.Save()
}

// Name is the iPod's name (the master playlist title).
func (d *Device) Name() string {
	if d.DB == nil || d.DB.Master() == nil {
		return "iPod"
	}
	if n := strings.TrimSpace(d.DB.Master().Name); n != "" {
		return n
	}
	return "iPod"
}

// Save writes the database atomically, keeping backups.
func (d *Device) Save() error {
	if d.ReadOnly != "" {
		return errors.New(d.ReadOnly)
	}
	if err := os.MkdirAll(d.itunesDir(), 0o755); err != nil {
		return err
	}
	if !d.backedUp {
		if err := d.backup(); err != nil {
			return fmt.Errorf("could not back up the database: %w", err)
		}
		d.backedUp = true
	}
	// Covers first: if the song database then fails to save, the
	// extra artwork entries are harmless.
	if err := d.saveArtwork(); err != nil {
		return fmt.Errorf("could not update the iPod's album art: %w", err)
	}
	data := d.DB.Bytes()
	tmp := d.dbPath() + ".podmanager-new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, d.dbPath()); err != nil {
		os.Remove(tmp)
		return err
	}
	if d.dirty {
		pc := filepath.Join(d.itunesDir(), "Play Counts")
		if _, err := os.Stat(pc); err == nil {
			os.Remove(pc + ".bak")
			os.Rename(pc, pc+".bak")
		}
		otgs, _ := filepath.Glob(filepath.Join(d.itunesDir(), "OTGPlaylistInfo*"))
		for _, p := range otgs {
			os.Remove(p)
		}
		d.dirty = false
	}
	return nil
}

func (d *Device) backup() error {
	src, err := os.Open(d.dbPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer src.Close()
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}
	// one copy on the iPod itself
	if err := os.WriteFile(d.dbPath()+".podmanager.bak", data, 0o644); err != nil {
		return err
	}
	// and dated copies on the computer
	if d.backupDir != "" {
		dir := filepath.Join(d.backupDir, safeName(d.Name()))
		if err := os.MkdirAll(dir, 0o755); err == nil {
			name := "iTunesDB-" + time.Now().Format("2006-01-02_150405")
			os.WriteFile(filepath.Join(dir, name), data, 0o644)
			old, _ := filepath.Glob(filepath.Join(dir, "iTunesDB-*"))
			sort.Strings(old)
			for len(old) > 10 {
				os.Remove(old[0])
				old = old[1:]
			}
		}
	}
	return nil
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, s)
	if s == "" {
		return "iPod"
	}
	return s
}

// AbsPath returns the absolute path of a track's file.
func (d *Device) AbsPath(t *itdb.Track) string {
	return filepath.Join(d.Root, filepath.FromSlash(t.RelPath()))
}

// AllocateFile chooses a new, unused location for a song with the given
// extension. It returns the absolute path and the iTunesDB location string.
func (d *Device) AllocateFile(ext string) (string, string, error) {
	music := filepath.Join(d.Root, "iPod_Control", "Music")
	entries, _ := os.ReadDir(music)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && len(e.Name()) == 3 && (e.Name()[0] == 'F' || e.Name()[0] == 'f') {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		for i := 0; i < 50; i++ {
			n := fmt.Sprintf("F%02d", i)
			if err := os.MkdirAll(filepath.Join(music, n), 0o755); err != nil {
				return "", "", err
			}
			dirs = append(dirs, n)
		}
	}
	ext = strings.ToLower(ext)
	const letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for attempt := 0; attempt < 1000; attempt++ {
		var rb [5]byte
		rand.Read(rb[:])
		dir := dirs[int(rb[0])%len(dirs)]
		name := make([]byte, 4)
		for i := range name {
			name[i] = letters[int(rb[i+1])%len(letters)]
		}
		fn := string(name) + ext
		abs := filepath.Join(music, dir, fn)
		if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
			return abs, ":iPod_Control:Music:" + dir + ":" + fn, nil
		}
	}
	return "", "", errors.New("could not find a free file name on the iPod")
}

// MusicBytes returns the total size of all songs in the database.
func (d *Device) MusicBytes() uint64 {
	var n uint64
	if d.DB != nil {
		for _, t := range d.DB.Tracks {
			if !t.IsVideo() {
				n += uint64(t.Size())
			}
		}
	}
	return n
}

// VideoBytes is the space taken by videos.
func (d *Device) VideoBytes() uint64 {
	var n uint64
	if d.DB != nil {
		for _, t := range d.DB.Tracks {
			if t.IsVideo() {
				n += uint64(t.Size())
			}
		}
	}
	return n
}

var fwGUIDExt = regexp.MustCompile(`(?s)<key>FireWireGUID</key>\s*<string>(?:0x)?([0-9A-Fa-f]+)</string>`)

// FirewireGUID returns the iPod's FireWire GUID from SysInfo or
// SysInfoExtended, upper-case hex without "0x". The iPod also reports it as
// its USB serial number, which is how its USB interfaces are matched to the
// drive. Empty if unknown.
func FirewireGUID(root string) string {
	dev := filepath.Join(root, "iPod_Control", "Device")
	if b, err := os.ReadFile(filepath.Join(dev, "SysInfo")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if ok && strings.EqualFold(strings.TrimSpace(k), "FirewireGuid") {
				v = strings.TrimSpace(v)
				v = strings.TrimPrefix(strings.TrimPrefix(v, "0x"), "0X")
				if v != "" {
					return strings.ToUpper(v)
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(dev, "SysInfoExtended")); err == nil {
		if m := fwGUIDExt.FindSubmatch(b); m != nil {
			return strings.ToUpper(string(m[1]))
		}
	}
	return ""
}
