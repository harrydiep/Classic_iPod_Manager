package itdb

import "fmt"

// MergePlayCounts applies an iPod "Play Counts" file (written by the iPod
// when tracks are played) to the database. Entries are in mhlt order.
// It mirrors libgpod's behaviour.
func (db *DB) MergePlayCounts(data []byte) error {
	if len(data) < 16 || string(data[:4]) != "mhdp" {
		return fmt.Errorf("itdb: not a Play Counts file")
	}
	hl := int(le.Uint32(data[4:]))
	el := int(le.Uint32(data[8:]))
	n := int(le.Uint32(data[12:]))
	if hl < 0x60 || el < 0x0C {
		return fmt.Errorf("itdb: unsupported Play Counts layout")
	}
	for i := 0; i < n && i < len(db.Tracks); i++ {
		off := hl + i*el
		if off+el > len(data) {
			break
		}
		e := data[off : off+el]
		t := db.Tracks[i]
		pc := le.Uint32(e[0:])
		played := le.Uint32(e[4:])
		bookmark := le.Uint32(e[8:])
		if el >= 0x10 {
			r := le.Uint32(e[12:])
			if r <= 100 && uint8(r) != t.Rating() {
				t.setU8(0x79, t.Rating()) // app rating keeps the previous value
				t.SetRating(uint8(r))
			}
		}
		if played != 0 {
			t.setU32(0x58, played)
		}
		if bookmark != 0 {
			t.setU32(0x6C, bookmark)
		}
		t.setU32(0x50, t.PlayCount()+pc)
		t.setU32(0x54, pc)
		if pc != 0 {
			t.setU8(0xB2, 1)
		}
		if el >= 0x1C {
			skips := le.Uint32(e[20:])
			t.setU32(0x9C, t.SkipCount()+skips)
			if ls := le.Uint32(e[24:]); ls != 0 {
				t.setU32(0xA0, ls)
			}
		}
	}
	return nil
}

// ImportOTG converts an "OTGPlaylistInfo" file (On-The-Go playlist made on
// the iPod) into a regular playlist. Returns false if it was empty.
func (db *DB) ImportOTG(data []byte, name string) (bool, error) {
	if len(data) < 16 || string(data[:4]) != "mhpo" {
		return false, fmt.Errorf("itdb: not an OTG playlist file")
	}
	hl := int(le.Uint32(data[4:]))
	el := int(le.Uint32(data[8:]))
	n := int(le.Uint32(data[12:]))
	if hl < 0x14 || el < 4 {
		return false, fmt.Errorf("itdb: unsupported OTG layout")
	}
	var items []Item
	for i := 0; i < n; i++ {
		off := hl + i*el
		if off+4 > len(data) {
			break
		}
		idx := int(le.Uint32(data[off:]))
		if idx < len(db.Tracks) {
			items = append(items, Item{TrackID: db.Tracks[idx].ID()})
		}
	}
	if len(items) == 0 {
		return false, nil
	}
	ph := make([]byte, 108)
	copy(ph, "mhyp")
	le.PutUint64(ph[0x1C:], randU64())
	le.PutUint16(ph[0x28:], 1)
	db.Playlists = append(db.Playlists, &Playlist{header: ph, Name: name, Items: items})
	return true, nil
}
