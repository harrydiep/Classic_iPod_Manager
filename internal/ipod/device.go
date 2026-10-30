// Package ipod handles an iPod's file system layout: finding the database,
// loading and saving it safely, and allocating file names for new songs.
package ipod

import (
	"bytes"
	"compress/zlib"
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

// Device is an opened iPod.
type Device struct {
	Root     string
	DB       *itdb.DB
	Model    Model
	Firmware string
	ReadOnly string // non-empty: reason the database must not be written
	// FirewireID signs the database of iPods that need hash58.
	FirewireID string
	compressed bool // read from iTunesCDB

	backupDir string
	backedUp  bool
	dirty     bool // Play Counts/OTG merged, needs cleaning on save

	artOnce sync.Once
	art     map[uint64][]thumbRef

	unknownArtOnce sync.Once
	unknownArt     []ArtFormat

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

// SerialLookup, when set, returns the USB serial number of the drive at
// root. iPods report their FireWire ID as their serial number, which is how
// it's found when SysInfo doesn't list it. (Set by the Windows build.)
var SerialLookup func(root string) string

// firewireID finds the iPod's FireWire ID: SysInfo, SysInfoExtended, or the
// drive's USB serial number.
func firewireID(root string) string {
	if id := firewireIDFromFiles(root); id != "" {
		return id
	}
	if SerialLookup != nil {
		s := strings.TrimSpace(SerialLookup(root))
		if len(s) == 16 {
			if _, err := parseFirewireID(s); err == nil {
				return strings.ToUpper(s)
			}
		}
	}
	return ""
}

// readDB reads the iTunesDB, or the compressed iTunesCDB that the 5th
// generation nano and later keep instead.
func (d *Device) readDB() ([]byte, error) {
	data, err := os.ReadFile(d.dbPath())
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return data, err
	}
	c, cerr := os.ReadFile(filepath.Join(d.itunesDir(), "iTunesCDB"))
	if cerr != nil {
		return nil, err
	}
	d.compressed = true
	return inflateCDB(c)
}

// inflateCDB expands an iTunesCDB: an mhbd header followed by the rest of
// the database compressed with zlib.
func inflateCDB(c []byte) ([]byte, error) {
	if len(c) < 0xA9 || string(c[:4]) != "mhbd" {
		return nil, errors.New("the iPod's compressed music database isn't valid")
	}
	hl := int(binary.LittleEndian.Uint32(c[4:]))
	if hl < 0xA9 || hl > len(c) {
		return nil, errors.New("the iPod's compressed music database isn't valid")
	}
	zr, err := zlib.NewReader(bytes.NewReader(c[hl:]))
	if err != nil {
		return nil, fmt.Errorf("the iPod's compressed music database can't be read: %w", err)
	}
	defer zr.Close()
	body, err := io.ReadAll(io.LimitReader(zr, 1<<30))
	if err != nil {
		return nil, fmt.Errorf("the iPod's compressed music database can't be read: %w", err)
	}
	out := make([]byte, 0, hl+len(body))
	out = append(out, c[:hl]...)
	out = append(out, body...)
	binary.LittleEndian.PutUint32(out[8:], uint32(len(out)))
	out[0xA8] = 0 // compression flag
	return out, nil
}

// Open loads the iPod's database. backupDir is where database backups are
// kept on the computer.
func Open(root, backupDir string) (*Device, error) {
	d := &Device{Root: root, backupDir: backupDir}
	d.Model, d.Firmware = ReadModel(root)
	data, err := d.readDB()
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
	d.checkWritable(data)
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
	if d.Model.shuffleDB > 0 {
		if b, err := os.ReadFile(filepath.Join(d.itunesDir(), "iTunesStats")); err == nil && len(b) > 0 {
			if mergeShuffleStats(db, b, d.Model.shuffleDB == 1) {
				d.dirty = true
			}
		}
	}
	return d, nil
}

// checkWritable decides whether Classic iPod Manager may change this iPod,
// and explains why not.
func (d *Device) checkWritable(data []byte) {
	m := &d.Model
	// For iPods the model table doesn't know, the database itself says how
	// it's signed.
	if c := dbChecksum(data); c > m.checksum {
		m.checksum = c
	}
	what := m.Name
	if m.Generation != "" {
		what += " (" + m.Generation + ")"
	}
	switch {
	case d.compressed:
		d.ReadOnly = what + " keeps its library in a newer, compressed database that only iTunes or Apple Music can write, so Classic iPod Manager can show it and copy songs off it, but not change it."
	case m.checksum == ChecksumHash72 || m.checksum == ChecksumHashAB:
		d.ReadOnly = what + " protects its library with a signature only iTunes or Apple Music can make, so Classic iPod Manager can show it and copy songs off it, but not change it."
	case !m.Supported:
		d.ReadOnly = what + " can't be changed by Classic iPod Manager."
	case m.checksum == ChecksumHash58:
		d.FirewireID = firewireID(d.Root)
		if d.FirewireID == "" {
			d.ReadOnly = what + " checks a signature made from its FireWire ID before it accepts changes, and Classic iPod Manager couldn't read the ID. " +
				"Add a line \"FirewireGuid: 0x…\" with the iPod's 16-digit ID to iPod_Control\\Device\\SysInfo on the iPod, or connect it once to iTunes or Apple Music, which writes SysInfoExtended."
		}
	}
}

// Initialize creates the folder structure and an empty database.
func Initialize(root, name, backupDir string) (*Device, error) {
	m, _ := ReadModel(root)
	dirs := m.musicDirs
	if dirs <= 0 {
		dirs = 20
	}
	for i := 0; i < dirs; i++ {
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
	d.checkWritable(nil)
	if d.ReadOnly != "" {
		return nil, errors.New(d.ReadOnly)
	}
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
	if d.Model.checksum == ChecksumHash58 {
		if err := signHash58(data, d.FirewireID); err != nil {
			return fmt.Errorf("could not sign the database for this iPod: %w", err)
		}
	}
	if err := writeAtomic(d.dbPath(), data); err != nil {
		return err
	}
	// the shuffle plays from iTunesSD; its old stats and play order belong
	// to the previous song list
	if d.Model.shuffleDB > 0 {
		sd := itunesSDv1(d.DB)
		if d.Model.shuffleDB == 2 {
			sd = itunesSDv2(d.DB)
		}
		if err := writeAtomic(filepath.Join(d.itunesDir(), "iTunesSD"), sd); err != nil {
			return fmt.Errorf("could not write the shuffle's song list: %w", err)
		}
		os.Remove(filepath.Join(d.itunesDir(), "iTunesStats"))
		os.Remove(filepath.Join(d.itunesDir(), "iTunesShuffle"))
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
func FirewireGUID(root string) string { return firewireID(root) }

func firewireIDFromFiles(root string) string {
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
