// Package itdb reads and writes the iTunesDB database used by the iPod
// classic family (tested against the layout used by the iPod Video / 5th
// generation, which does not require a database checksum).
//
// The writer is conservative: everything it does not understand inside a
// track or playlist is carried over byte-for-byte, so ratings, play counts,
// artwork links, smart playlist rules and so on survive a round trip.
package itdb

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf16"
)

// MHOD string types.
const (
	MhodTitle           = 1
	MhodPath            = 2
	MhodAlbum           = 3
	MhodArtist          = 4
	MhodGenre           = 5
	MhodFiletype        = 6
	MhodComment         = 8
	MhodCategory        = 9
	MhodComposer        = 12
	MhodGrouping        = 13
	MhodDescription     = 14
	MhodAlbumArtist     = 22
	MhodSortArtist      = 23
	MhodSortTitle       = 27
	MhodSortAlbum       = 28
	MhodSortAlbumArtist = 29
	MhodSortComposer    = 30
	mhodSplPref         = 50
	mhodLibIndex        = 52
	mhodLibJump         = 53
	mhodPlaylistPos     = 100
)

// stringMhod reports whether an mhod type carries a UTF-16/UTF-8 string
// with the standard 16 byte string sub-header.
func stringMhod(t uint32) bool {
	switch {
	case t >= 1 && t <= 14 && t != 10 && t != 11:
		return true
	case t >= 18 && t <= 31:
		return true
	case t >= 200 && t <= 202, t == 300:
		return true
	}
	return false
}

const macEpochOffset = 2082844800 // seconds between 1904-01-01 and 1970-01-01

// MacTime converts a time to the iPod's "Mac" timestamp (seconds since
// 1904, local wall-clock time).
func MacTime(t time.Time) uint32 {
	if t.IsZero() {
		return 0
	}
	_, off := t.Zone()
	return uint32(t.Unix() + macEpochOffset + int64(off))
}

// FromMacTime is the inverse of MacTime.
func FromMacTime(v uint32) time.Time {
	if v == 0 {
		return time.Time{}
	}
	u := int64(v) - macEpochOffset
	t := time.Unix(u, 0)
	_, off := t.Zone()
	return time.Unix(u-int64(off), 0)
}

// Mhod is one data object attached to a track or playlist.
type Mhod struct {
	Type  uint32
	Str   string // valid when IsStr
	IsStr bool
	Raw   []byte // full raw chunk for non-string mhods
}

// Track is a single mhit record.
type Track struct {
	header []byte
	Mhods  []Mhod
}

// Playlist is a single mhyp record.
type Playlist struct {
	header []byte
	Name   string
	extra  [][]byte // raw mhods other than title/52/53
	Items  []Item
}

// Item is a playlist entry (mhip).
type Item struct {
	TrackID   uint32
	Timestamp uint32
}

// DB is an in-memory iTunesDB.
type DB struct {
	header    []byte
	Tracks    []*Track
	Playlists []*Playlist // Playlists[0] is the master (library) playlist
}

var le = binary.LittleEndian

// ---------------------------------------------------------------- tracks

func (t *Track) u8(off int) uint8 {
	if off < len(t.header) {
		return t.header[off]
	}
	return 0
}
func (t *Track) setU8(off int, v uint8) {
	if off < len(t.header) {
		t.header[off] = v
	}
}
func (t *Track) u16(off int) uint16 {
	if off+2 <= len(t.header) {
		return le.Uint16(t.header[off:])
	}
	return 0
}
func (t *Track) setU16(off int, v uint16) {
	if off+2 <= len(t.header) {
		le.PutUint16(t.header[off:], v)
	}
}
func (t *Track) u32(off int) uint32 {
	if off+4 <= len(t.header) {
		return le.Uint32(t.header[off:])
	}
	return 0
}
func (t *Track) setU32(off int, v uint32) {
	if off+4 <= len(t.header) {
		le.PutUint32(t.header[off:], v)
	}
}
func (t *Track) u64(off int) uint64 {
	if off+8 <= len(t.header) {
		return le.Uint64(t.header[off:])
	}
	return 0
}
func (t *Track) setU64(off int, v uint64) {
	if off+8 <= len(t.header) {
		le.PutUint64(t.header[off:], v)
	}
}

func (t *Track) ID() uint32            { return t.u32(0x10) }
func (t *Track) SetID(v uint32)        { t.setU32(0x10, v) }
func (t *Track) FileTypeCode() uint32  { return t.u32(0x18) }
func (t *Track) Compilation() bool     { return t.u8(0x1E) != 0 }
func (t *Track) SetCompilation(b bool) { t.setU8(0x1E, b2u8(b)) }

// Rating is stars*20 (0..100).
func (t *Track) Rating() uint8            { return t.u8(0x1F) }
func (t *Track) SetRating(v uint8)        { t.setU8(0x1F, v) }
func (t *Track) TimeModified() uint32     { return t.u32(0x20) }
func (t *Track) SetTimeModified(v uint32) { t.setU32(0x20, v) }
func (t *Track) Size() uint32             { return t.u32(0x24) }
func (t *Track) SetSize(v uint32) {
	t.setU32(0x24, v)
	if len(t.header) >= 0x130 {
		t.setU32(0x12C, v) // "file size again" field used by newer layouts
	}
}
func (t *Track) LengthMs() uint32        { return t.u32(0x28) }
func (t *Track) SetLengthMs(v uint32)    { t.setU32(0x28, v) }
func (t *Track) TrackNumber() uint32     { return t.u32(0x2C) }
func (t *Track) SetTrackNumber(v uint32) { t.setU32(0x2C, v) }
func (t *Track) TrackCount() uint32      { return t.u32(0x30) }
func (t *Track) SetTrackCount(v uint32)  { t.setU32(0x30, v) }
func (t *Track) Year() uint32            { return t.u32(0x34) }
func (t *Track) SetYear(v uint32)        { t.setU32(0x34, v) }
func (t *Track) Bitrate() uint32         { return t.u32(0x38) }
func (t *Track) SampleRate() uint32      { return t.u32(0x3C) >> 16 }
func (t *Track) PlayCount() uint32       { return t.u32(0x50) }
func (t *Track) SetPlayCount(v uint32)   { t.setU32(0x50, v) }
func (t *Track) LastPlayed() uint32      { return t.u32(0x58) }
func (t *Track) DiscNumber() uint32      { return t.u32(0x5C) }
func (t *Track) SetDiscNumber(v uint32)  { t.setU32(0x5C, v) }
func (t *Track) DiscCount() uint32       { return t.u32(0x60) }
func (t *Track) SetDiscCount(v uint32)   { t.setU32(0x60, v) }
func (t *Track) DateAdded() uint32       { return t.u32(0x68) }
func (t *Track) DBID() uint64            { return t.u64(0x70) }
func (t *Track) BPM() uint16             { return t.u16(0x7A) }
func (t *Track) SetBPM(v uint16)         { t.setU16(0x7A, v) }
func (t *Track) SkipCount() uint32       { return t.u32(0x9C) }
func (t *Track) MediaType() uint32       { return t.u32(0xD0) }
func (t *Track) HeaderLen() int          { return len(t.header) }

// HasArtwork reports whether the iPod should show a cover for the track.
func (t *Track) HasArtwork() bool { return t.u8(0xA4) == 1 && t.u16(0x7C) > 0 }

// ArtworkLink is the ArtworkDB image (mhii) ID the track points at.
func (t *Track) ArtworkLink() uint32 { return t.u32(0x160) }

// SetArtwork records that the track has a cover in the ArtworkDB: the image
// ID, and the size of the source picture. It mirrors what libgpod writes.
func (t *Track) SetArtwork(mhii uint32, sourceSize uint32) {
	t.setU16(0x7C, 1)
	t.setU32(0x80, sourceSize)
	t.setU8(0xA4, 1)
	t.setU32(0x160, mhii)
}

// ClearArtwork marks the track as having no cover.
func (t *Track) ClearArtwork() {
	t.setU16(0x7C, 0)
	t.setU32(0x80, 0)
	t.setU8(0xA4, 2)
	t.setU32(0x160, 0)
}

// FileInfo describes a song file for SetFile.
type FileInfo struct {
	Location     string // iPod style path
	FileTypeCode string // 4 chars, e.g. "M4A "
	Kind         string
	Size         uint32
	LengthMs     uint32
	Bitrate      uint32
	SampleRate   uint32
	VBR          bool
	IsAAC        bool
	SampleCount  uint64
}

// SetFile points the track at a different file (after converting it),
// keeping its identity, ratings, play counts and playlists.
func (t *Track) SetFile(fi FileInfo) {
	code := []byte((fi.FileTypeCode + "    ")[:4])
	t.setU32(0x18, uint32(code[0])<<24|uint32(code[1])<<16|uint32(code[2])<<8|uint32(code[3]))
	if fi.IsAAC {
		t.setU8(0x1C, 0)
		t.setU8(0x1D, 0)
	} else {
		t.setU8(0x1C, 1)
		t.setU8(0x1D, b2u8(fi.VBR))
	}
	t.SetSize(fi.Size)
	t.SetLengthMs(fi.LengthMs)
	t.setU32(0x38, fi.Bitrate)
	t.setU32(0x3C, fi.SampleRate<<16)
	t.setU32(0x88, math.Float32bits(float32(fi.SampleRate)))
	t.setU64(0xBC, fi.SampleCount)
	// gapless playback data belongs to the old encoding
	t.setU32(0xB8, 0) // pregap
	t.setU32(0xC8, 0) // postgap
	t.setU32(0xF8, 0)
	t.setU32(0xFC, 0)
	t.setU32(0x100, 0)
	t.SetStr(MhodPath, fi.Location)
	t.SetStr(MhodFiletype, fi.Kind)
	t.SetTimeModified(MacTime(time.Now()))
}

// Str returns the string mhod of the given type ("" when absent).
func (t *Track) Str(typ uint32) string {
	for _, m := range t.Mhods {
		if m.IsStr && m.Type == typ {
			return m.Str
		}
	}
	return ""
}

// SetStr sets (or removes, when s is empty) a string mhod.
func (t *Track) SetStr(typ uint32, s string) {
	for i, m := range t.Mhods {
		if m.Type == typ {
			if s == "" && typ != MhodPath {
				t.Mhods = append(t.Mhods[:i], t.Mhods[i+1:]...)
			} else {
				t.Mhods[i] = Mhod{Type: typ, Str: s, IsStr: true}
			}
			return
		}
	}
	if s != "" {
		t.Mhods = append(t.Mhods, Mhod{Type: typ, Str: s, IsStr: true})
	}
}

func (t *Track) Title() string       { return t.Str(MhodTitle) }
func (t *Track) Artist() string      { return t.Str(MhodArtist) }
func (t *Track) Album() string       { return t.Str(MhodAlbum) }
func (t *Track) AlbumArtist() string { return t.Str(MhodAlbumArtist) }
func (t *Track) Genre() string       { return t.Str(MhodGenre) }
func (t *Track) Composer() string    { return t.Str(MhodComposer) }
func (t *Track) Comment() string     { return t.Str(MhodComment) }
func (t *Track) Grouping() string    { return t.Str(MhodGrouping) }
func (t *Track) Kind() string        { return t.Str(MhodFiletype) }

// Location returns the iPod-style path, e.g. ":iPod_Control:Music:F01:ABCD.mp3".
func (t *Track) Location() string { return t.Str(MhodPath) }

// RelPath converts Location() into a slash separated path relative to the
// iPod root (e.g. "iPod_Control/Music/F01/ABCD.mp3").
func (t *Track) RelPath() string {
	return strings.TrimPrefix(strings.ReplaceAll(t.Location(), ":", "/"), "/")
}

// NewTrackInfo describes a track to be created.
type NewTrackInfo struct {
	Location                                                              string // iPod style path (":iPod_Control:Music:F00:ABCD.mp3")
	FileTypeCode                                                          string // 4 chars, e.g. "MP3 "
	Kind                                                                  string // "MPEG audio file"
	Size                                                                  uint32
	LengthMs                                                              uint32
	Bitrate                                                               uint32
	SampleRate                                                            uint32
	VBR                                                                   bool
	IsAAC                                                                 bool
	SampleCount                                                           uint64
	MediaType                                                             uint32 // 1 audio, 8 audiobook
	RememberPos                                                           bool
	Title, Artist, Album, AlbumArtist, Genre, Composer, Comment, Grouping string
	TrackNumber, TrackCount, DiscNumber, DiscCount, Year                  uint32
	BPM                                                                   uint16
	Compilation                                                           bool
}

// NewTrack creates and appends a track, assigning a fresh ID and DBID.
func (db *DB) NewTrack(info NewTrackInfo) *Track {
	hl := 0x184
	for _, tr := range db.Tracks {
		if tr.HeaderLen() > hl {
			hl = tr.HeaderLen()
		}
	}
	t := &Track{header: make([]byte, hl)}
	copy(t.header, "mhit")
	le.PutUint32(t.header[4:], uint32(hl))
	t.SetID(db.nextTrackID())
	t.setU32(0x14, 1) // visible
	code := []byte((info.FileTypeCode + "    ")[:4])
	t.setU32(0x18, uint32(code[0])<<24|uint32(code[1])<<16|uint32(code[2])<<8|uint32(code[3]))
	if info.IsAAC {
		t.setU8(0x1C, 0)
		t.setU8(0x1D, 0)
	} else {
		t.setU8(0x1C, 1)
		if info.VBR {
			t.setU8(0x1D, 1)
		} else {
			t.setU8(0x1D, 0)
		}
	}
	t.SetCompilation(info.Compilation)
	now := MacTime(time.Now())
	t.SetTimeModified(now)
	t.SetSize(info.Size)
	t.SetLengthMs(info.LengthMs)
	t.SetTrackNumber(info.TrackNumber)
	t.SetTrackCount(info.TrackCount)
	t.SetYear(info.Year)
	t.setU32(0x38, info.Bitrate)
	t.setU32(0x3C, info.SampleRate<<16)
	t.SetDiscNumber(info.DiscNumber)
	t.SetDiscCount(info.DiscCount)
	t.setU32(0x68, now) // date added
	dbid := randU64()
	t.setU64(0x70, dbid)
	t.setU8(0x78, 0) // checked (0 = checked)
	t.SetBPM(info.BPM)
	t.setU32(0x88, math.Float32bits(float32(info.SampleRate)))
	t.setU8(0xA4, 2) // has artwork: 2 = no
	t.setU16(0x7E, 0xFFFF)
	if info.RememberPos {
		t.setU8(0xA6, 1)
		t.setU8(0xA5, 1) // skip when shuffling
	}
	t.setU64(0xA8, dbid)
	t.setU8(0xB2, 2) // "not played" mark
	t.setU64(0xBC, info.SampleCount)
	mt := info.MediaType
	if mt == 0 {
		mt = 1
	}
	t.setU32(0xD0, mt)
	t.SetStr(MhodTitle, info.Title)
	t.SetStr(MhodPath, info.Location)
	t.SetStr(MhodAlbum, info.Album)
	t.SetStr(MhodArtist, info.Artist)
	t.SetStr(MhodGenre, info.Genre)
	t.SetStr(MhodFiletype, info.Kind)
	t.SetStr(MhodComment, info.Comment)
	t.SetStr(MhodComposer, info.Composer)
	t.SetStr(MhodGrouping, info.Grouping)
	t.SetStr(MhodAlbumArtist, info.AlbumArtist)
	db.Tracks = append(db.Tracks, t)
	if len(db.Playlists) > 0 {
		db.Playlists[0].Items = append(db.Playlists[0].Items, Item{TrackID: t.ID(), Timestamp: now})
	}
	return t
}

func (db *DB) nextTrackID() uint32 {
	var max uint32 = 51 // iTunes starts somewhere above 50; any unique value works
	for _, t := range db.Tracks {
		if t.ID() > max {
			max = t.ID()
		}
	}
	return max + 1
}

// TrackByID returns the track with the given ID or nil.
func (db *DB) TrackByID(id uint32) *Track {
	for _, t := range db.Tracks {
		if t.ID() == id {
			return t
		}
	}
	return nil
}

// RemoveTracks removes the tracks with the given IDs from the database and
// from every playlist. It returns the removed tracks.
func (db *DB) RemoveTracks(ids []uint32) []*Track {
	set := make(map[uint32]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	var removed []*Track
	kept := db.Tracks[:0]
	for _, t := range db.Tracks {
		if set[t.ID()] {
			removed = append(removed, t)
		} else {
			kept = append(kept, t)
		}
	}
	db.Tracks = kept
	for _, p := range db.Playlists {
		items := p.Items[:0]
		for _, it := range p.Items {
			if !set[it.TrackID] {
				items = append(items, it)
			}
		}
		p.Items = items
	}
	return removed
}

// ------------------------------------------------------------- playlists

func (p *Playlist) IsMaster() bool { return len(p.header) > 0x14 && p.header[0x14] == 1 }
func (p *Playlist) IsPodcast() bool {
	return len(p.header) >= 0x2C && le.Uint16(p.header[0x2A:]) == 1
}
func (p *Playlist) IsSmart() bool {
	for _, m := range p.extra {
		if len(m) >= 16 && le.Uint32(m[12:]) == mhodSplPref {
			return true
		}
	}
	return false
}
func (p *Playlist) ID() uint64 {
	if len(p.header) >= 0x24 {
		return le.Uint64(p.header[0x1C:])
	}
	return 0
}

// Master returns the master playlist.
func (db *DB) Master() *Playlist {
	if len(db.Playlists) == 0 {
		return nil
	}
	return db.Playlists[0]
}

// ------------------------------------------------------------ new / misc

// New creates an empty database with a master playlist named name.
func New(name string) *DB {
	h := make([]byte, 0xF4)
	copy(h, "mhbd")
	le.PutUint32(h[4:], 0xF4)
	le.PutUint32(h[0x0C:], 1)
	le.PutUint32(h[0x10:], 0x19) // iTunes 7.4 era version
	le.PutUint32(h[0x14:], 3)
	le.PutUint64(h[0x18:], randU64())
	le.PutUint16(h[0x20:], 2) // platform: Windows
	le.PutUint64(h[0x24:], randU64())
	copy(h[0x46:], "en")
	le.PutUint64(h[0x48:], randU64())
	db := &DB{header: h}
	ph := make([]byte, 108)
	copy(ph, "mhyp")
	ph[0x14] = 1
	le.PutUint32(ph[0x18:], MacTime(time.Now()))
	le.PutUint64(ph[0x1C:], randU64())
	le.PutUint16(ph[0x28:], 1)
	db.Playlists = []*Playlist{{header: ph, Name: name}}
	return db
}

func randU64() uint64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return le.Uint64(b[:])
}

func b2u8(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

// --------------------------------------------------------------- parsing

type parseErr string

func (e parseErr) Error() string { return "itdb: " + string(e) }

// ErrNotITunesDB is returned when the data does not start with an mhbd chunk.
var ErrNotITunesDB = errors.New("itdb: not an iTunesDB file")

// Parse decodes an iTunesDB file.
func Parse(data []byte) (*DB, error) {
	if len(data) < 16 || string(data[:4]) != "mhbd" {
		return nil, ErrNotITunesDB
	}
	hl := int(le.Uint32(data[4:]))
	total := int(le.Uint32(data[8:]))
	if hl < 0x68 || hl > len(data) || total > len(data) || total < hl {
		return nil, parseErr("bad mhbd header")
	}
	db := &DB{header: append([]byte(nil), data[:hl]...)}
	var plType2, plType3 []byte
	off := hl
	for off+16 <= total {
		if string(data[off:off+4]) != "mhsd" {
			return nil, parseErr(fmt.Sprintf("expected mhsd at 0x%x", off))
		}
		shl := int(le.Uint32(data[off+4:]))
		stot := int(le.Uint32(data[off+8:]))
		styp := le.Uint32(data[off+12:])
		if stot < shl || off+stot > len(data) {
			return nil, parseErr("bad mhsd length")
		}
		body := data[off+shl : off+stot]
		switch styp {
		case 1:
			if err := db.parseTracks(body); err != nil {
				return nil, err
			}
		case 2:
			plType2 = body
		case 3:
			plType3 = body
		}
		off += stot
	}
	pl := plType2
	if pl == nil {
		pl = plType3
	}
	if pl != nil {
		if err := db.parsePlaylists(pl); err != nil {
			return nil, err
		}
	}
	if len(db.Playlists) == 0 || !db.Playlists[0].IsMaster() {
		// Find the master playlist and move it to the front, or create one.
		idx := -1
		for i, p := range db.Playlists {
			if p.IsMaster() {
				idx = i
				break
			}
		}
		if idx > 0 {
			m := db.Playlists[idx]
			db.Playlists = append(db.Playlists[:idx], db.Playlists[idx+1:]...)
			db.Playlists = append([]*Playlist{m}, db.Playlists...)
		} else if idx < 0 {
			n := New("iPod")
			db.Playlists = append(n.Playlists, db.Playlists...)
		}
	}
	return db, nil
}

func (db *DB) parseTracks(b []byte) error {
	if len(b) < 12 || string(b[:4]) != "mhlt" {
		return parseErr("expected mhlt")
	}
	hl := int(le.Uint32(b[4:]))
	n := int(le.Uint32(b[8:]))
	off := hl
	for i := 0; i < n; i++ {
		if off+16 > len(b) || string(b[off:off+4]) != "mhit" {
			return parseErr(fmt.Sprintf("expected mhit #%d", i))
		}
		thl := int(le.Uint32(b[off+4:]))
		ttot := int(le.Uint32(b[off+8:]))
		nm := int(le.Uint32(b[off+12:]))
		if thl < 0x9C || ttot < thl || off+ttot > len(b) {
			return parseErr("bad mhit length")
		}
		t := &Track{header: append([]byte(nil), b[off:off+thl]...)}
		mo := off + thl
		end := off + ttot
		for j := 0; j < nm && mo+12 <= end; j++ {
			m, size, err := parseMhod(b[mo:end])
			if err != nil {
				return err
			}
			t.Mhods = append(t.Mhods, m)
			mo += size
		}
		db.Tracks = append(db.Tracks, t)
		off += ttot
	}
	return nil
}

func parseMhod(b []byte) (Mhod, int, error) {
	if len(b) < 24 || string(b[:4]) != "mhod" {
		return Mhod{}, 0, parseErr("expected mhod")
	}
	hl := int(le.Uint32(b[4:]))
	tot := int(le.Uint32(b[8:]))
	typ := le.Uint32(b[12:])
	if tot < hl || tot > len(b) || hl < 24 {
		return Mhod{}, 0, parseErr("bad mhod length")
	}
	raw := b[:tot]
	if stringMhod(typ) && tot >= hl+16 {
		body := raw[hl:]
		enc := le.Uint32(body[0:])
		n := int(le.Uint32(body[4:]))
		if 16+n <= len(body) {
			s := body[16 : 16+n]
			var str string
			if enc == 2 {
				str = string(s)
			} else {
				u := make([]uint16, n/2)
				for i := range u {
					u[i] = le.Uint16(s[2*i:])
				}
				str = string(utf16.Decode(u))
			}
			return Mhod{Type: typ, Str: str, IsStr: true}, tot, nil
		}
	}
	return Mhod{Type: typ, Raw: append([]byte(nil), raw...)}, tot, nil
}

func (db *DB) parsePlaylists(b []byte) error {
	if len(b) < 12 || string(b[:4]) != "mhlp" {
		return parseErr("expected mhlp")
	}
	hl := int(le.Uint32(b[4:]))
	n := int(le.Uint32(b[8:]))
	off := hl
	for i := 0; i < n; i++ {
		if off+16 > len(b) || string(b[off:off+4]) != "mhyp" {
			return parseErr("expected mhyp")
		}
		phl := int(le.Uint32(b[off+4:]))
		ptot := int(le.Uint32(b[off+8:]))
		if phl < 0x30 || ptot < phl || off+ptot > len(b) {
			return parseErr("bad mhyp length")
		}
		p := &Playlist{header: append([]byte(nil), b[off:off+phl]...)}
		mo := off + phl
		end := off + ptot
		seenItem := false
		for mo+12 <= end {
			tag := string(b[mo : mo+4])
			ctot := int(le.Uint32(b[mo+8:]))
			switch tag {
			case "mhod":
				m, size, err := parseMhod(b[mo:end])
				if err != nil {
					return err
				}
				if !seenItem {
					switch {
					case m.IsStr && m.Type == MhodTitle:
						p.Name = m.Str
					case m.Type == mhodLibIndex || m.Type == mhodLibJump:
						// regenerated on write
					default:
						p.extra = append(p.extra, m.Raw)
					}
				}
				ctot = size
			case "mhip":
				if ctot < 0x1C || mo+ctot > end {
					return parseErr("bad mhip")
				}
				seenItem = true
				tid := le.Uint32(b[mo+0x18:])
				var ts uint32
				if mo+0x20 <= end {
					ts = le.Uint32(b[mo+0x1C:])
				}
				if tid != 0 { // tid 0 = podcast group header
					p.Items = append(p.Items, Item{TrackID: tid, Timestamp: ts})
				}
			default:
				return parseErr("unexpected chunk " + tag + " in playlist")
			}
			if ctot <= 0 {
				return parseErr("zero length chunk in playlist")
			}
			mo += ctot
		}
		db.Playlists = append(db.Playlists, p)
		off += ptot
	}
	return nil
}

// --------------------------------------------------------------- writing

type buf struct{ bytes.Buffer }

func (w *buf) u8(v uint8)   { w.WriteByte(v) }
func (w *buf) u16(v uint16) { var b [2]byte; le.PutUint16(b[:], v); w.Write(b[:]) }
func (w *buf) u32(v uint32) { var b [4]byte; le.PutUint32(b[:], v); w.Write(b[:]) }
func (w *buf) u64(v uint64) { var b [8]byte; le.PutUint64(b[:], v); w.Write(b[:]) }
func (w *buf) zeros(n int)  { w.Write(make([]byte, n)) }
func (w *buf) patch32(at int, v uint32) {
	le.PutUint32(w.Bytes()[at:], v)
}

func writeStringMhod(w *buf, typ uint32, s string) {
	u := utf16.Encode([]rune(s))
	n := len(u) * 2
	w.WriteString("mhod")
	w.u32(24)
	w.u32(uint32(40 + n))
	w.u32(typ)
	w.zeros(8)
	w.u32(1) // UTF-16
	w.u32(uint32(n))
	w.u32(1)
	w.u32(0)
	for _, c := range u {
		w.u16(c)
	}
}

// Bytes serialises the database.
func (db *DB) Bytes() []byte {
	w := &buf{}
	h := append([]byte(nil), db.header...)
	le.PutUint32(h[0x14:], 3)
	w.Write(h)

	// --- mhsd type 1: tracks
	sd := w.Len()
	writeMhsd(w, 1)
	w.WriteString("mhlt")
	w.u32(92)
	w.u32(uint32(len(db.Tracks)))
	w.zeros(80)
	for _, t := range db.Tracks {
		start := w.Len()
		w.Write(t.header)
		count := 0
		for _, m := range t.Mhods {
			if m.IsStr {
				if m.Str == "" && m.Type != MhodPath {
					continue
				}
				writeStringMhod(w, m.Type, m.Str)
			} else {
				w.Write(m.Raw)
			}
			count++
		}
		w.patch32(start+8, uint32(w.Len()-start))
		w.patch32(start+12, uint32(count))
	}
	w.patch32(sd+8, uint32(w.Len()-sd))

	// Master playlist always lists every track, in mhlt order.
	if m := db.Master(); m != nil {
		old := map[uint32]uint32{}
		for _, it := range m.Items {
			old[it.TrackID] = it.Timestamp
		}
		m.Items = m.Items[:0]
		for _, t := range db.Tracks {
			m.Items = append(m.Items, Item{TrackID: t.ID(), Timestamp: old[t.ID()]})
		}
	}

	nextID := uint32(0)
	for _, t := range db.Tracks {
		if t.ID() > nextID {
			nextID = t.ID()
		}
	}
	nextID += 1000

	// --- mhsd type 3 (podcast-grouped) and type 2 (plain)
	for _, typ := range []uint32{3, 2} {
		sd := w.Len()
		writeMhsd(w, typ)
		w.WriteString("mhlp")
		w.u32(92)
		w.u32(uint32(len(db.Playlists)))
		w.zeros(80)
		for _, p := range db.Playlists {
			db.writePlaylist(w, p, typ == 3 && p.IsPodcast(), &nextID)
		}
		w.patch32(sd+8, uint32(w.Len()-sd))
	}
	w.patch32(8, uint32(w.Len()))
	return w.Bytes()
}

func writeMhsd(w *buf, typ uint32) {
	w.WriteString("mhsd")
	w.u32(96)
	w.u32(0)
	w.u32(typ)
	w.zeros(80)
}

func (db *DB) writePlaylist(w *buf, p *Playlist, podcastGroups bool, nextID *uint32) {
	start := w.Len()
	h := append([]byte(nil), p.header...)
	if len(h) < 0x30 {
		h = append(h, make([]byte, 0x30-len(h))...)
	}
	le.PutUint32(h[4:], uint32(len(h)))
	w.Write(h)
	nm := 1
	writeStringMhod(w, MhodTitle, p.Name)
	for _, raw := range p.extra {
		w.Write(raw)
		nm++
	}
	if p.IsMaster() && len(db.Tracks) > 0 {
		nm += db.writeLibraryIndices(w)
	}
	items := 0
	writeItem := func(groupFlag, groupID, trackID, ts, groupRef, pos uint32, title string) {
		s := w.Len()
		w.WriteString("mhip")
		w.u32(76)
		w.u32(0)
		w.u32(1)
		w.u32(groupFlag)
		w.u32(groupID)
		w.u32(trackID)
		w.u32(ts)
		w.u32(groupRef)
		w.zeros(40)
		if title != "" || trackID == 0 {
			writeStringMhod(w, MhodTitle, title)
		} else {
			w.WriteString("mhod")
			w.u32(24)
			w.u32(44)
			w.u32(mhodPlaylistPos)
			w.zeros(8)
			w.u32(pos)
			w.zeros(16)
		}
		w.patch32(s+8, uint32(w.Len()-s))
		items++
	}
	if podcastGroups {
		var order []string
		groups := map[string][]Item{}
		for _, it := range p.Items {
			album := ""
			if t := db.TrackByID(it.TrackID); t != nil {
				album = t.Album()
			}
			if _, ok := groups[album]; !ok {
				order = append(order, album)
			}
			groups[album] = append(groups[album], it)
		}
		for _, album := range order {
			*nextID++
			gid := *nextID
			writeItem(256, gid, 0, 0, 0, 0, album)
			for _, it := range groups[album] {
				*nextID++
				writeItem(0, *nextID, it.TrackID, it.Timestamp, gid, *nextID, "")
			}
		}
	} else {
		for i, it := range p.Items {
			writeItem(0, 0, it.TrackID, it.Timestamp, 0, uint32(i), "")
		}
	}
	w.patch32(start+8, uint32(w.Len()-start))
	w.patch32(start+12, uint32(nm))
	w.patch32(start+16, uint32(items))
}

// --------------------------------------------------- library index (52/53)

type sortKeys struct {
	idx                                   int
	title, album, artist, genre, composer string
	disc, track                           uint32
}

// foldAccents maps common accented Latin letters to their base letter so
// that "Über" sorts (and jumps) with the U's, as the iPod expects.
var foldAccents = strings.NewReplacer(
	"à", "a", "á", "a", "â", "a", "ã", "a", "ä", "a", "å", "a", "æ", "ae",
	"ç", "c", "è", "e", "é", "e", "ê", "e", "ë", "e", "ì", "i", "í", "i",
	"î", "i", "ï", "i", "ð", "d", "ñ", "n", "ò", "o", "ó", "o", "ô", "o",
	"õ", "o", "ö", "o", "ø", "o", "ù", "u", "ú", "u", "û", "u", "ü", "u",
	"ý", "y", "ÿ", "y", "ß", "ss", "ā", "a", "ă", "a", "ą", "a", "ć", "c",
	"č", "c", "ď", "d", "ē", "e", "ę", "e", "ě", "e", "ğ", "g", "ī", "i",
	"ı", "i", "ł", "l", "ń", "n", "ň", "n", "ō", "o", "ő", "o", "œ", "oe",
	"ř", "r", "ś", "s", "ş", "s", "š", "s", "ť", "t", "ū", "u", "ů", "u",
	"ű", "u", "ź", "z", "ż", "z", "ž", "z",
)

func collateKey(s string) string {
	return foldAccents.Replace(strings.ToLower(strings.TrimSpace(s)))
}

func jumpLetter(s string) uint16 {
	for _, r := range foldAccents.Replace(strings.ToLower(s)) {
		if isAlnum(r) {
			if r >= '0' && r <= '9' {
				return '0'
			}
			u := []rune(strings.ToUpper(string(r)))[0]
			enc := utf16.Encode([]rune{u})
			return enc[0]
		}
	}
	return '0'
}

func isAlnum(r rune) bool {
	return (r >= '0' && r <= '9') || strings.ToUpper(string(r)) != strings.ToLower(string(r))
}

func pick(sortVal, val string) string {
	if sortVal != "" {
		return sortVal
	}
	return val
}

func stripThe(s string) string {
	if len(s) > 4 && strings.EqualFold(s[:4], "the ") {
		return s[4:]
	}
	return s
}

func (db *DB) writeLibraryIndices(w *buf) int {
	keys := make([]sortKeys, len(db.Tracks))
	for i, t := range db.Tracks {
		keys[i] = sortKeys{
			idx:      i,
			title:    pick(t.Str(MhodSortTitle), t.Title()),
			album:    pick(t.Str(MhodSortAlbum), t.Album()),
			artist:   pick(t.Str(MhodSortArtist), stripThe(t.Artist())),
			genre:    t.Genre(),
			composer: pick(t.Str(MhodSortComposer), t.Composer()),
			disc:     t.DiscNumber(),
			track:    t.TrackNumber(),
		}
	}
	type sorter struct {
		typ    uint32
		letter func(k *sortKeys) string
		less   func(a, b *sortKeys) int
	}
	cmpS := func(a, b string) int { return strings.Compare(collateKey(a), collateKey(b)) }
	cmpU := func(a, b uint32) int {
		if a < b {
			return -1
		} else if a > b {
			return 1
		}
		return 0
	}
	albumTail := func(a, b *sortKeys) int {
		if c := cmpS(a.album, b.album); c != 0 {
			return c
		}
		if c := cmpU(a.disc, b.disc); c != 0 {
			return c
		}
		if c := cmpU(a.track, b.track); c != 0 {
			return c
		}
		return cmpS(a.title, b.title)
	}
	sorters := []sorter{
		{3, func(k *sortKeys) string { return k.title }, func(a, b *sortKeys) int { return cmpS(a.title, b.title) }},
		{5, func(k *sortKeys) string { return k.artist }, func(a, b *sortKeys) int {
			if c := cmpS(a.artist, b.artist); c != 0 {
				return c
			}
			return albumTail(a, b)
		}},
		{4, func(k *sortKeys) string { return k.album }, albumTail},
		{7, func(k *sortKeys) string { return k.genre }, func(a, b *sortKeys) int {
			if c := cmpS(a.genre, b.genre); c != 0 {
				return c
			}
			if c := cmpS(a.artist, b.artist); c != 0 {
				return c
			}
			return albumTail(a, b)
		}},
		{0x12, func(k *sortKeys) string { return k.composer }, func(a, b *sortKeys) int {
			if c := cmpS(a.composer, b.composer); c != 0 {
				return c
			}
			return albumTail(a, b)
		}},
	}
	order := make([]*sortKeys, len(keys))
	for _, s := range sorters {
		for i := range keys {
			order[i] = &keys[i]
		}
		stableSort(order, s.less)
		// mhod 52
		w.WriteString("mhod")
		w.u32(24)
		w.u32(uint32(72 + 4*len(order)))
		w.u32(mhodLibIndex)
		w.zeros(8)
		w.u32(s.typ)
		w.u32(uint32(len(order)))
		w.zeros(40)
		type jump struct {
			letter       uint16
			start, count uint32
		}
		var jumps []jump
		for i, k := range order {
			w.u32(uint32(k.idx))
			l := jumpLetter(s.letter(k))
			if len(jumps) == 0 || jumps[len(jumps)-1].letter != l {
				jumps = append(jumps, jump{letter: l, start: uint32(i)})
			}
			jumps[len(jumps)-1].count++
		}
		// mhod 53
		w.WriteString("mhod")
		w.u32(24)
		w.u32(uint32(40 + 12*len(jumps)))
		w.u32(mhodLibJump)
		w.zeros(8)
		w.u32(s.typ)
		w.u32(uint32(len(jumps)))
		w.zeros(8)
		for _, j := range jumps {
			w.u16(j.letter)
			w.u16(0)
			w.u32(j.start)
			w.u32(j.count)
		}
	}
	return 2 * len(sorters)
}

func stableSort(a []*sortKeys, cmp func(x, y *sortKeys) int) {
	// simple merge sort (stable)
	if len(a) < 2 {
		return
	}
	tmp := make([]*sortKeys, len(a))
	var rec func(lo, hi int)
	rec = func(lo, hi int) {
		if hi-lo < 2 {
			return
		}
		mid := (lo + hi) / 2
		rec(lo, mid)
		rec(mid, hi)
		i, j, k := lo, mid, lo
		for i < mid && j < hi {
			if cmp(a[j], a[i]) < 0 {
				tmp[k] = a[j]
				j++
			} else {
				tmp[k] = a[i]
				i++
			}
			k++
		}
		for i < mid {
			tmp[k] = a[i]
			i++
			k++
		}
		for j < hi {
			tmp[k] = a[j]
			j++
			k++
		}
		copy(a[lo:hi], tmp[lo:hi])
	}
	rec(0, len(a))
}
