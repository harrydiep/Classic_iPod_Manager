package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"podmanager/internal/ipod"
	"podmanager/internal/itdb"
)

func TestMoveItems(t *testing.T) {
	items := func(ids ...uint32) []itdb.Item {
		var out []itdb.Item
		for _, id := range ids {
			out = append(out, itdb.Item{TrackID: id})
		}
		return out
	}
	str := func(it []itdb.Item) string {
		s := ""
		for _, x := range it {
			s += fmt.Sprint(x.TrackID)
		}
		return s
	}
	cases := []struct {
		pos  []int
		to   int
		want string
	}{
		{[]int{0}, 3, "23145"},     // first item before the 4th
		{[]int{4}, 0, "51234"},     // last to the top
		{[]int{1, 3}, 5, "13524"},  // two to the end
		{[]int{3, 1}, 0, "24135"},  // order kept, whatever order given
		{[]int{2}, 2, "12345"},     // onto itself
		{[]int{0, 1}, 1, "12345"},  // a block before its own second item
		{[]int{9, -1}, 0, "12345"}, // out of range ignored
	}
	for _, c := range cases {
		if got := str(moveItems(items(1, 2, 3, 4, 5), c.pos, c.to)); got != c.want {
			t.Errorf("move %v to %d: %s, want %s", c.pos, c.to, got, c.want)
		}
	}
}

func TestPlaylistAPI(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	a := New(&fakePlat{root: root}, t.TempDir(), func(Event) {})
	if _, err := a.CreatePlaylist("x", nil); err == nil {
		t.Fatal("no iPod open")
	}
	if _, err := a.Initialize(root, "P iPod"); err != nil {
		t.Fatal(err)
	}
	var ids []uint32
	a.withDB(func() {
		for i := 0; i < 4; i++ {
			tr := a.dev.DB.NewTrack(itdb.NewTrackInfo{Location: fmt.Sprintf(":iPod_Control:Music:F00:S%d.mp3", i), FileTypeCode: "MP3 ",
				Kind: "MPEG audio file", Title: fmt.Sprintf("Song %d", i), LengthMs: 1000})
			ids = append(ids, tr.ID())
		}
	})
	id, err := a.CreatePlaylist("  Mix  ", []uint32{ids[0], ids[1]})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := a.CreatePlaylist("Mix", nil) // name taken → "Mix 2"
	if n, err := a.AddToPlaylist(id, []uint32{ids[1], ids[2], ids[3], 99999}); err != nil || n != 2 {
		t.Fatalf("add: %d %v", n, err)
	}
	if err := a.MovePlaylistItems(id, []int{3}, 0); err != nil { // song 3 to the top
		t.Fatal(err)
	}
	if err := a.RemoveFromPlaylist(id, []int{1}); err != nil { // song 0 out
		t.Fatal(err)
	}
	if err := a.RenamePlaylist(id, "Road Mix"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreatePlaylist("   ", nil); err == nil {
		t.Fatal("empty name accepted")
	}
	lib, _ := a.Library()
	if len(lib.Playlists) != 2 || lib.Playlists[1].Name != "Mix 2" || lib.Playlists[1].ID != id2 {
		t.Fatalf("playlists: %+v", lib.Playlists)
	}
	pl := lib.Playlists[0]
	if pl.Name != "Road Mix" || !pl.Editable || fmt.Sprint(pl.TrackIDs) != fmt.Sprint([]uint32{ids[3], ids[1], ids[2]}) {
		t.Fatalf("mix: %+v", pl)
	}
	if err := a.DeletePlaylist(id2); err != nil {
		t.Fatal(err)
	}
	// the master playlist can't be touched
	var master string
	a.withDB(func() { master = fmt.Sprint(a.dev.DB.Master().ID()) })
	if a.DeletePlaylist(master) == nil || a.RenamePlaylist(master, "x") == nil {
		t.Fatal("master playlist edited")
	}
	if _, err := a.AddToPlaylist(master, ids); err == nil {
		t.Fatal("master playlist edited")
	}
	// saved to the iPod
	d, err := ipod.Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DB.Playlists) != 2 || d.DB.Playlists[1].Name != "Road Mix" || len(d.DB.Playlists[1].Items) != 3 {
		t.Fatalf("on disk: %d playlists", len(d.DB.Playlists))
	}
	// removing a song from the iPod takes it out of playlists too
	a.withDB(func() { a.dev.DB.RemoveTracks([]uint32{ids[1]}) })
	lib, _ = a.Library()
	if len(lib.Playlists[0].TrackIDs) != 2 {
		t.Fatalf("after removing a song: %+v", lib.Playlists[0])
	}
}
