package app

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	"podmanager/internal/ipod"
	"podmanager/internal/itdb"
)

// Playlist editing. Changes are saved to the iPod straight away; the UI
// reloads the library afterwards.

// editPlaylists runs fn on the open iPod's database and saves it.
func (a *App) editPlaylists(fn func(d *ipod.Device) error) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	d := a.dev
	if d == nil {
		return errors.New("no iPod is open")
	}
	if d.ReadOnly != "" {
		return errors.New(d.ReadOnly)
	}
	if err := fn(d); err != nil {
		return err
	}
	return d.Save()
}

func findPlaylist(d *ipod.Device, id string) (*itdb.Playlist, error) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return nil, errors.New("unknown playlist")
	}
	p := d.DB.PlaylistByID(n)
	if p == nil {
		return nil, errors.New("the playlist no longer exists")
	}
	return p, nil
}

func editable(d *ipod.Device, id string) (*itdb.Playlist, error) {
	p, err := findPlaylist(d, id)
	if err != nil {
		return nil, err
	}
	switch {
	case p.IsMaster():
		return nil, errors.New("the iPod's library can't be edited like a playlist")
	case p.IsSmart():
		return nil, errors.New("smart playlists fill themselves; their songs can't be changed by hand")
	case p.IsPodcast():
		return nil, errors.New("the Podcasts playlist can't be edited")
	}
	return p, nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("the playlist needs a name")
	}
	if len([]rune(name)) > 255 {
		name = string([]rune(name)[:255])
	}
	return name, nil
}

// uniqueName adds " 2", " 3"… if a playlist with the name exists.
func uniqueName(d *ipod.Device, name string) string {
	taken := map[string]bool{}
	for _, p := range d.DB.Playlists[1:] {
		taken[strings.ToLower(p.Name)] = true
	}
	n, base := 2, name
	for taken[strings.ToLower(name)] {
		name = base + " " + strconv.Itoa(n)
		n++
	}
	return name
}

// CreatePlaylist makes a playlist, optionally with songs, and returns its id.
func (a *App) CreatePlaylist(name string, ids []uint32) (string, error) {
	name, err := cleanName(name)
	if err != nil {
		return "", err
	}
	var id string
	err = a.editPlaylists(func(d *ipod.Device) error {
		p := d.DB.NewPlaylist(uniqueName(d, name))
		for _, tid := range ids {
			if d.DB.TrackByID(tid) != nil {
				p.Add(tid)
			}
		}
		id = strconv.FormatUint(p.ID(), 10)
		return nil
	})
	return id, err
}

// RenamePlaylist changes a playlist's name.
func (a *App) RenamePlaylist(id, name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	return a.editPlaylists(func(d *ipod.Device) error {
		p, err := findPlaylist(d, id)
		if err != nil {
			return err
		}
		if p.IsMaster() {
			return errors.New("rename the iPod itself to change this name")
		}
		p.Name = name
		return nil
	})
}

// DeletePlaylist removes a playlist; its songs stay on the iPod.
func (a *App) DeletePlaylist(id string) error {
	return a.editPlaylists(func(d *ipod.Device) error {
		p, err := findPlaylist(d, id)
		if err != nil {
			return err
		}
		if !d.DB.DeletePlaylist(p) {
			return errors.New("the iPod's library can't be deleted")
		}
		return nil
	})
}

// AddToPlaylist appends songs, skipping ones already in it. It returns how
// many were added.
func (a *App) AddToPlaylist(id string, ids []uint32) (int, error) {
	added := 0
	err := a.editPlaylists(func(d *ipod.Device) error {
		p, err := editable(d, id)
		if err != nil {
			return err
		}
		in := map[uint32]bool{}
		for _, it := range p.Items {
			in[it.TrackID] = true
		}
		for _, tid := range ids {
			if !in[tid] && d.DB.TrackByID(tid) != nil {
				p.Add(tid)
				in[tid] = true
				added++
			}
		}
		return nil
	})
	return added, err
}

// RemoveFromPlaylist takes the entries at the given positions out of a
// playlist (the songs stay on the iPod).
func (a *App) RemoveFromPlaylist(id string, positions []int) error {
	return a.editPlaylists(func(d *ipod.Device) error {
		p, err := editable(d, id)
		if err != nil {
			return err
		}
		drop := map[int]bool{}
		for _, i := range positions {
			drop[i] = true
		}
		kept := p.Items[:0]
		for i, it := range p.Items {
			if !drop[i] {
				kept = append(kept, it)
			}
		}
		p.Items = kept
		return nil
	})
}

// MovePlaylistItems moves the entries at positions so they sit, in their
// current order, just before the entry now at position to (len = the end).
func (a *App) MovePlaylistItems(id string, positions []int, to int) error {
	return a.editPlaylists(func(d *ipod.Device) error {
		p, err := editable(d, id)
		if err != nil {
			return err
		}
		p.Items = moveItems(p.Items, positions, to)
		return nil
	})
}

func moveItems(items []itdb.Item, positions []int, to int) []itdb.Item {
	sel := map[int]bool{}
	for _, i := range positions {
		if i >= 0 && i < len(items) {
			sel[i] = true
		}
	}
	idx := make([]int, 0, len(sel))
	for i := range sel {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	var moved, rest []itdb.Item
	insert := 0
	for i, it := range items {
		if sel[i] {
			moved = append(moved, it)
			continue
		}
		if i < to {
			insert++
		}
		rest = append(rest, it)
	}
	out := make([]itdb.Item, 0, len(items))
	out = append(out, rest[:insert]...)
	out = append(out, moved...)
	return append(out, rest[insert:]...)
}
