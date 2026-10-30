package ipod

import (
	"bytes"
	"encoding/binary"
	"strings"
	"unicode/utf16"

	"podmanager/internal/itdb"
)

// The iPod shuffle has no screen and doesn't read the iTunesDB: it plays
// from iPod_Control/iTunes/iTunesSD, a simpler list of the songs (and, from
// the 3rd generation, the playlists). Classic iPod Manager writes the
// iTunesDB as for any iPod and the iTunesSD next to it, in the layout
// libgpod uses. The shuffle keeps play counts in iTunesStats, in iTunesSD
// order; they're merged into the library when the iPod is opened.

// shuffleFileType is the iTunesSD file type: 1 MP3, 2 AAC, 4 WAV.
func shuffleFileType(kind string) uint32 {
	k := strings.ToLower(kind)
	switch {
	case strings.Contains(k, "aac") || strings.Contains(k, "mp4") || strings.Contains(k, "m4a") || strings.Contains(k, "lossless"):
		return 2
	case strings.Contains(k, "wav"):
		return 4
	}
	return 1
}

func shufflePath(t *itdb.Track) string {
	return strings.ReplaceAll(t.Location(), ":", "/")
}

func put24be(b *bytes.Buffer, v uint32) { b.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)}) }

// itunesSDv1 is the 1st/2nd generation format: 24-bit big-endian numbers,
// 558-byte entries with UTF-16 paths.
func itunesSDv1(db *itdb.DB) []byte {
	var b bytes.Buffer
	put24be(&b, uint32(len(db.Tracks)))
	put24be(&b, 0x010600)
	put24be(&b, 0x12)
	put24be(&b, 0)
	put24be(&b, 0)
	put24be(&b, 0)
	for _, t := range db.Tracks {
		put24be(&b, 0x22e)
		put24be(&b, 0x5aa501)
		put24be(&b, t.StartMs()/256)
		put24be(&b, 0)
		put24be(&b, 0)
		put24be(&b, t.StopMs()/256)
		put24be(&b, 0)
		put24be(&b, 0)
		put24be(&b, uint32((int(t.Volume())+255)*201/511)) // -255..255 → 0..200
		put24be(&b, shuffleFileType(t.Kind()))
		put24be(&b, 0x200)
		path := utf16.Encode([]rune(shufflePath(t)))
		if len(path) > 261 {
			path = path[:261]
		}
		for _, c := range path {
			binary.Write(&b, binary.LittleEndian, c)
		}
		b.Write(make([]byte, (261-len(path))*2))
		b.WriteByte(boolByte(!t.SkipWhenShuffling()))
		b.WriteByte(boolByte(t.RememberPosition()))
		b.WriteByte(0)
	}
	return b.Bytes()
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}

type leBuf struct{ bytes.Buffer }

func (w *leBuf) u8(v byte)    { w.WriteByte(v) }
func (w *leBuf) u16(v uint16) { var b [2]byte; binary.LittleEndian.PutUint16(b[:], v); w.Write(b[:]) }
func (w *leBuf) u32(v uint32) { var b [4]byte; binary.LittleEndian.PutUint32(b[:], v); w.Write(b[:]) }
func (w *leBuf) u64(v uint64) { var b [8]byte; binary.LittleEndian.PutUint64(b[:], v); w.Write(b[:]) }
func (w *leBuf) patch(at int, v uint32) {
	binary.LittleEndian.PutUint32(w.Bytes()[at:], v)
}

func isSpokenWord(t *itdb.Track) bool {
	return t.MediaType()&(itdb.MediaAudiobook|itdb.MediaPodcast) != 0
}

// itunesSDv2 is the 3rd/4th generation format (bdhs/hths/rths/hphs/lphs),
// little-endian. It expects db.Bytes to have numbered the albums and
// artists.
func itunesSDv2(db *itdb.DB) []byte {
	w := &leBuf{}
	tracks := db.Tracks
	index := map[uint32]int{}
	spoken := 0
	for i, t := range tracks {
		index[t.ID()] = i
		if isSpokenWord(t) {
			spoken++
		}
	}
	// bdhs: the main header (64 bytes)
	w.WriteString("bdhs")
	w.u32(0x02000003)
	w.u32(64)
	w.u32(uint32(len(tracks)))
	w.u32(0) // non-empty playlists, below
	w.u32(0)
	w.u32(0)
	w.u8(0) // no volume limit
	w.u8(1) // VoiceOver on
	w.u16(0)
	w.u32(uint32(len(tracks) - spoken)) // +0x20
	w.u32(0)                            // +0x24 track list offset
	w.u32(0)                            // +0x28 playlist list offset
	w.Write(make([]byte, 20))

	// hths: the track list
	w.patch(0x24, uint32(w.Len()))
	hths := w.Len()
	w.WriteString("hths")
	w.u32(uint32(20 + 4*len(tracks)))
	w.u32(uint32(len(tracks)))
	w.u32(0)
	w.u32(0)
	offs := w.Len()
	w.Write(make([]byte, 4*len(tracks)))
	_ = hths
	for i, t := range tracks {
		w.patch(offs+4*i, uint32(w.Len()))
		start := w.Len()
		w.WriteString("rths")
		w.u32(0) // length, below
		w.u32(t.StartMs())
		w.u32(t.StopMs())
		w.u32(uint32(t.Volume()))
		w.u32(shuffleFileType(t.Kind()))
		path := shufflePath(t)
		if len(path) > 256 {
			path = path[:256]
		}
		w.WriteString(path)
		w.Write(make([]byte, 256-len(path)))
		w.u32(t.BookmarkMs())
		w.u8(boolByte(!t.SkipWhenShuffling())) // 1: plays in shuffle mode
		w.u8(boolByte(t.RememberPosition()))
		w.u8(0)
		w.u8(0)
		w.u32(t.Pregap())
		w.u32(t.Postgap())
		w.u32(uint32(t.SampleCount()))
		w.u32(0)
		w.u32(t.GaplessData())
		w.u32(0)
		w.u32(t.AlbumID())
		w.u16(uint16(t.TrackNumber()))
		w.u16(uint16(t.DiscNumber()))
		w.u32(0)
		w.u32(0)
		w.u64(t.DBID())
		w.u32(t.ArtistID())
		w.Write(make([]byte, 32))
		w.patch(start+4, uint32(w.Len()-start))
	}

	// hphs: the playlists that have songs
	var pls []*itdb.Playlist
	podcasts, masters, books := 0, 0, 0
	for _, p := range db.Playlists {
		if len(p.Items) == 0 {
			continue
		}
		pls = append(pls, p)
		switch {
		case p.IsMaster():
			masters++
		case p.IsPodcast():
			podcasts++
		case p.Name == "Audiobooks":
			books++
		}
	}
	w.patch(0x10, uint32(len(pls)))
	w.patch(0x28, uint32(w.Len()))
	w.WriteString("hphs")
	w.u32(uint32(20 + 4*len(pls)))
	w.u16(uint16(len(pls)))
	w.u16(0)
	w.u16(uint16(len(pls) - podcasts))
	w.u16(uint16(masters))
	w.u16(uint16(len(pls) - books))
	w.u16(0)
	poffs := w.Len()
	w.Write(make([]byte, 4*len(pls)))
	for i, p := range pls {
		w.patch(poffs+4*i, uint32(w.Len()))
		start := w.Len()
		typ := uint32(2)
		switch {
		case p.IsMaster():
			typ = 1
		case p.IsPodcast():
			typ = 3
		case p.Name == "Audiobooks":
			typ = 4
		}
		var members []uint32
		spokenHere := 0
		for _, it := range p.Items {
			if n, ok := index[it.TrackID]; ok {
				members = append(members, uint32(n))
				if isSpokenWord(tracks[n]) {
					spokenHere++
				}
			}
		}
		w.WriteString("lphs")
		w.u32(0)
		w.u32(uint32(len(members)))
		w.u32(uint32(len(members) - spokenHere))
		if typ == 1 {
			w.u64(0)
		} else {
			w.u64(p.ID())
		}
		w.u32(typ)
		w.Write(make([]byte, 16))
		for _, n := range members {
			w.u32(n)
		}
		w.patch(start+4, uint32(w.Len()-start))
	}
	return w.Bytes()
}

// mergeShuffleStats adds the play counts and bookmarks the shuffle recorded
// in iTunesStats (one entry per iTunesSD song, in iTunesDB order). It
// reports whether anything was read.
func mergeShuffleStats(db *itdb.DB, b []byte, v1 bool) bool {
	type stat struct{ plays, skips, bookmark uint32 }
	var stats []stat
	if v1 {
		le24 := func(o int) uint32 { return uint32(b[o]) | uint32(b[o+1])<<8 | uint32(b[o+2])<<16 }
		if len(b) < 6 {
			return false
		}
		n := int(le24(0))
		off := 6
		for i := 0; i < n; i++ {
			if off+18 > len(b) {
				return false
			}
			l := int(le24(off))
			if l < 18 {
				return false
			}
			stats = append(stats, stat{plays: le24(off + 12), skips: le24(off + 15), bookmark: le24(off + 3)})
			off += l
		}
	} else {
		if len(b) < 8 {
			return false
		}
		n := int(binary.LittleEndian.Uint32(b))
		off := 8
		for i := 0; i < n; i++ {
			if off+32 > len(b) {
				return false
			}
			l := int(binary.LittleEndian.Uint32(b[off:]))
			if l < 32 {
				return false
			}
			stats = append(stats, stat{bookmark: binary.LittleEndian.Uint32(b[off+4:]),
				plays: binary.LittleEndian.Uint32(b[off+8:]), skips: binary.LittleEndian.Uint32(b[off+16:])})
			off += l
		}
	}
	if len(stats) != len(db.Tracks) {
		return false // written for a different song list
	}
	for i, s := range stats {
		t := db.Tracks[i]
		if s.plays > 0 {
			t.AddPlays(s.plays)
		}
		if s.skips > 0 {
			t.AddSkips(s.skips)
		}
		if s.bookmark > 0 {
			t.SetBookmarkMs(s.bookmark)
		}
	}
	return true
}
