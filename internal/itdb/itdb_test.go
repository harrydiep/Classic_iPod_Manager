package itdb

import (
	"encoding/binary"
	"os"
	"testing"
)

func sample() *DB {
	db := New("Test iPod")
	names := []struct{ t, a, al string }{
		{"Zebra Song", "The Beatles", "Abbey Road"},
		{"apple tree", "Abba", "Gold"},
		{"Über Alles", "Björk", "Post"},
		{"42 Things", "ZZ Top", "Eliminator"},
	}
	for i, n := range names {
		db.NewTrack(NewTrackInfo{
			Location: ":iPod_Control:Music:F00:T" + string(rune('A'+i)) + ".mp3", FileTypeCode: "MP3 ",
			Kind: "MPEG audio file", Size: uint32(1000 + i), LengthMs: 200000, Bitrate: 192, SampleRate: 44100,
			Title: n.t, Artist: n.a, Album: n.al, Genre: "Rock", TrackNumber: uint32(i + 1), TrackCount: 12, Year: 2001,
		})
	}
	return db
}

func TestRoundTrip(t *testing.T) {
	db := sample()
	// a user playlist
	ph := make([]byte, 108)
	copy(ph, "mhyp")
	db.Playlists = append(db.Playlists, &Playlist{header: ph, Name: "Mix", Items: []Item{{TrackID: db.Tracks[2].ID()}, {TrackID: db.Tracks[0].ID()}}})
	data := db.Bytes()
	if out := os.Getenv("ITDB_TEST_OUT"); out != "" {
		os.WriteFile(out, data, 0o644)
	}
	db2, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(db2.Tracks) != 4 || len(db2.Playlists) != 2 {
		t.Fatalf("got %d tracks %d playlists", len(db2.Tracks), len(db2.Playlists))
	}
	if db2.Tracks[2].Title() != "Über Alles" || db2.Tracks[2].Artist() != "Björk" {
		t.Fatalf("strings lost: %q %q", db2.Tracks[2].Title(), db2.Tracks[2].Artist())
	}
	if db2.Master().Name != "Test iPod" || len(db2.Master().Items) != 4 {
		t.Fatal("master playlist wrong")
	}
	if p := db2.Playlists[1]; p.Name != "Mix" || len(p.Items) != 2 || p.Items[0].TrackID != db.Tracks[2].ID() {
		t.Fatal("user playlist wrong")
	}
	// byte-identical on second write
	if string(db2.Bytes()) != string(data) {
		t.Fatal("second serialisation differs")
	}
}

func TestEditRemove(t *testing.T) {
	db := sample()
	id := db.Tracks[1].ID()
	db.Tracks[0].SetStr(MhodTitle, "Renamed")
	db.Tracks[0].SetStr(MhodGenre, "")
	db.Tracks[0].SetRating(80)
	db.RemoveTracks([]uint32{id})
	db2, err := Parse(db.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(db2.Tracks) != 3 || db2.TrackByID(id) != nil || len(db2.Master().Items) != 3 {
		t.Fatal("remove failed")
	}
	if db2.Tracks[0].Title() != "Renamed" || db2.Tracks[0].Genre() != "" || db2.Tracks[0].Rating() != 80 {
		t.Fatal("edit failed")
	}
}

func TestPlayCountsAndOTG(t *testing.T) {
	db := sample()
	pc := make([]byte, 0x60+2*0x1C)
	copy(pc, "mhdp")
	le := binary.LittleEndian
	le.PutUint32(pc[4:], 0x60)
	le.PutUint32(pc[8:], 0x1C)
	le.PutUint32(pc[12:], 2)
	le.PutUint32(pc[0x60+0x1C:], 5)      // track 2: 5 plays
	le.PutUint32(pc[0x60+0x1C+12:], 100) // 5 stars
	if err := db.MergePlayCounts(pc); err != nil {
		t.Fatal(err)
	}
	if db.Tracks[1].PlayCount() != 5 || db.Tracks[1].Rating() != 100 {
		t.Fatalf("playcounts not merged: %d %d", db.Tracks[1].PlayCount(), db.Tracks[1].Rating())
	}
	otg := make([]byte, 0x14+8)
	copy(otg, "mhpo")
	le.PutUint32(otg[4:], 0x14)
	le.PutUint32(otg[8:], 4)
	le.PutUint32(otg[12:], 2)
	le.PutUint32(otg[0x14:], 3)
	le.PutUint32(otg[0x18:], 0)
	ok, err := db.ImportOTG(otg, "On-The-Go 1")
	if err != nil || !ok {
		t.Fatal("otg", err)
	}
	p := db.Playlists[len(db.Playlists)-1]
	if p.Items[0].TrackID != db.Tracks[3].ID() || p.Items[1].TrackID != db.Tracks[0].ID() {
		t.Fatal("otg order wrong")
	}
}
