package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"podmanager/internal/iap"
	"podmanager/internal/ipod"
)

type fakePlat struct {
	root, pick, export string
	dragged            []DragFile
	serial             string
	battErr            error
	anyPick            []string
	opened             string
}

func (p *fakePlat) Roots() []string { return []string{p.root} }
func (p *fakePlat) VolumeInfo(string) (string, uint64, uint64, error) {
	return "IPOD", 30 << 30, 20 << 30, nil
}
func (p *fakePlat) Eject(string) error { return nil }
func (p *fakePlat) PickFiles() ([]string, error) {
	return []string{filepath.Join(p.pick, "cbr.mp3"), filepath.Join(p.pick, "aac.m4a")}, nil
}
func (p *fakePlat) PickFolder(t string) (string, error) {
	if t == "Choose where to copy the songs" {
		return p.export, nil
	}
	return p.pick, nil
}
func (p *fakePlat) Reveal(string) error        { return nil }
func (p *fakePlat) DragOut(f []DragFile) error { p.dragged = f; return nil }
func (p *fakePlat) PickAny(title string, f []FileFilter, multi bool) ([]string, error) {
	return p.anyPick, nil
}
func (p *fakePlat) OpenFolder(path string) error { p.opened = path; return nil }
func (p *fakePlat) BatteryReport(serial string) (iap.Battery, string, error) {
	b, err := p.Battery(serial)
	return b, "report for " + serial, err
}
func (p *fakePlat) Battery(serial string) (iap.Battery, error) {
	p.serial = serial
	if p.battErr != nil {
		return iap.Battery{}, p.battErr
	}
	return iap.Battery{State: iap.StateCharging, Level: 184}, nil
}

func waitDone(t *testing.T, ev chan Event) *JobResult {
	t.Helper()
	timeout := time.After(30 * time.Second)
	for {
		select {
		case e := <-ev:
			if e.Type == "done" {
				return e.Result
			}
		case <-timeout:
			t.Fatal("job timed out")
		}
	}
}

func TestEndToEnd(t *testing.T) {
	pick := os.Getenv("MEDIA_TEST_DIR")
	if pick == "" {
		t.Skip("MEDIA_TEST_DIR not set")
	}
	root := os.Getenv("APP_TEST_ROOT")
	if root == "" {
		root = t.TempDir()
	} else {
		os.RemoveAll(root)
	}
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("ModelNumStr: xA147\nvisibleBuildID: 0x01130000 (1.3)\n"), 0o644)
	plat := &fakePlat{root: root, pick: pick, export: t.TempDir()}
	ev := make(chan Event, 1000)
	a := New(plat, t.TempDir(), func(e Event) { ev <- e })

	devs, _ := a.Devices()
	if len(devs) != 1 || devs[0].HasDB || devs[0].Model.Name != "iPod 60GB" {
		t.Fatalf("devices: %+v", devs)
	}
	if _, err := a.Open(root); err == nil {
		t.Fatal("expected NO_DATABASE")
	}
	lib, err := a.Initialize(root, "Harry's iPod")
	if err != nil || lib.Device.Name != "Harry's iPod" {
		t.Fatal(err)
	}
	// bulk add a folder (contains a FLAC which must be rejected)
	if _, err := a.AddFolder(); err != nil {
		t.Fatal(err)
	}
	res := waitDone(t, ev)
	if res.Error != "" || res.OK != 8 || res.Skipped != 2 {
		t.Fatalf("add folder: %+v", res)
	}
	if len(res.Errors) != 1 {
		t.Fatalf("expected flac error: %+v", res.Errors)
	}
	// adding the same files again → duplicates skipped
	a.AddFiles()
	res = waitDone(t, ev)
	if res.OK != 0 || res.Skipped != 2 {
		t.Fatalf("dupes: %+v", res)
	}
	lib, _ = a.Library()
	if len(lib.Tracks) != 8 {
		t.Fatalf("tracks %d", len(lib.Tracks))
	}
	var mp3, m4a uint32
	for _, tr := range lib.Tracks {
		if tr.Title == "Test Song" && tr.Kind == "MPEG audio file" && mp3 == 0 {
			mp3 = tr.ID
		}
		if tr.Title == "AAC Song" {
			m4a = tr.ID
		}
	}
	// bulk edit with tag writing
	album, year, rating := "Edited Album", 1999, 80
	if err := a.Update([]uint32{mp3, m4a}, Patch{Album: &album, Year: &year, Rating: &rating}, true); err != nil {
		t.Fatal(err)
	}
	res = waitDone(t, ev)
	if res.Error != "" || res.OK != 2 || len(res.Errors) != 0 {
		t.Fatalf("update: %+v", res)
	}
	// reopen from disk and check
	a.Close()
	lib, err = a.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range lib.Tracks {
		if tr.ID == mp3 || tr.ID == m4a {
			if tr.Album != album || tr.Year != 1999 || tr.Rating != 80 {
				t.Fatalf("edit not persisted: %+v", tr)
			}
			st, _ := os.Stat(filepath.Join(root, filepath.FromSlash(tr.Path)))
			if st == nil || uint32(st.Size()) != tr.Size {
				t.Fatalf("size mismatch for %s", tr.Path)
			}
		}
	}
	// export two songs
	if _, err := a.Export([]uint32{mp3, m4a}); err != nil {
		t.Fatal(err)
	}
	res = waitDone(t, ev)
	if res.OK != 2 {
		t.Fatalf("export: %+v", res)
	}
	// remove one
	if err := a.Remove([]uint32{m4a}); err != nil {
		t.Fatal(err)
	}
	res = waitDone(t, ev)
	if res.OK != 1 {
		t.Fatalf("remove: %+v", res)
	}
	lib, _ = a.Library()
	if len(lib.Tracks) != 7 {
		t.Fatalf("after remove %d", len(lib.Tracks))
	}
	d, err := ipod.Open(root, "")
	if err != nil || len(d.DB.Tracks) != 7 || d.Name() != "Harry's iPod" {
		t.Fatalf("reopen: %v", err)
	}
	if err := a.Rename("Kitchen iPod"); err != nil {
		t.Fatal(err)
	}
}

func TestArtworkAPI(t *testing.T) {
	root := os.Getenv("IPOD_ART_ROOT")
	if root == "" {
		t.Skip("IPOD_ART_ROOT not set")
	}
	a := New(&fakePlat{root: root}, t.TempDir(), func(Event) {})
	lib, err := a.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	art, err := a.Artwork(lib.Tracks[0].ID)
	if err != nil || art == nil || art.Source != "ipod" || art.Width != 200 || len(art.DataURL) < 100 {
		t.Fatalf("artwork: %v %+v", err, art)
	}
	if art, _ := a.Artwork(lib.Tracks[4].ID); art != nil {
		t.Fatal("track 5 should have no artwork")
	}
}

func TestCheckPathsAndDragOut(t *testing.T) {
	pick := os.Getenv("MEDIA_TEST_DIR")
	if pick == "" {
		t.Skip("MEDIA_TEST_DIR not set")
	}
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "iPod_Control"), 0o755)
	plat := &fakePlat{root: root, pick: pick}
	ev := make(chan Event, 100)
	a := New(plat, t.TempDir(), func(e Event) { ev <- e })
	if c := a.CheckPaths([]string{filepath.Join(pick, "cbr.mp3")}); c.Reason == "" {
		t.Fatal("expected a refusal with no iPod open")
	}
	if _, err := a.Initialize(root, "Test"); err != nil {
		t.Fatal(err)
	}
	txt := filepath.Join(t.TempDir(), "notes.txt")
	os.WriteFile(txt, []byte("hello"), 0o644)
	c := a.CheckPaths([]string{filepath.Join(pick, "cbr.mp3"), filepath.Join(pick, "x.flac"), txt, filepath.Join(pick, "aac.m4a")})
	if len(c.Accepted) != 2 || c.Songs != 2 || len(c.Rejected) != 2 {
		t.Fatalf("check: %+v", c)
	}
	// a folder counts its songs and lists its unplayable files
	c = a.CheckPaths([]string{pick})
	if len(c.Accepted) != 1 || c.Songs != 10 || len(c.Rejected) != 1 {
		t.Fatalf("folder check: %+v", c)
	}
	a.AddPaths([]string{filepath.Join(pick, "cbr.mp3"), filepath.Join(pick, "vbr.mp3")})
	waitDone(t, ev)
	lib, _ := a.Library()
	if err := a.DragOut([]uint32{lib.Tracks[0].ID, lib.Tracks[1].ID}); err != nil {
		t.Fatal(err)
	}
	if len(plat.dragged) != 2 || plat.dragged[0].Name != "Tést Ärtist - Test Song.mp3" || plat.dragged[1].Name != "VBR Song.mp3" || plat.dragged[0].Size == 0 {
		t.Fatalf("drag: %+v", plat.dragged)
	}
}

func TestBattery(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("ModelNumStr: xA147\nFirewireGuid: 0x000a2700137f4c2d\n"), 0o644)
	plat := &fakePlat{root: root}
	a := New(plat, t.TempDir(), func(Event) {})
	v, _ := a.Battery(root)
	if !v.Available || v.Percent != 72 || !v.Charging || !v.External || v.State != "Charging" || plat.serial != "000A2700137F4C2D" {
		t.Fatalf("battery %+v serial %q", v, plat.serial)
	}
	// cached: a failure right after isn't seen until the cache expires
	plat.battErr = ErrNoAccessoryPort
	if v, _ := a.Battery(root); !v.Available {
		t.Fatal("expected cached result")
	}
	a.batt.at = time.Time{}
	if v, _ := a.Battery(root); v.Available || v.Percent != -1 || v.Reason == "" {
		t.Fatalf("unavailable: %+v", v)
	}
	if v, _ := a.Battery(""); v.Available {
		t.Fatal("no root")
	}
	// the report bypasses the cache and refreshes it
	plat.battErr = nil
	r, err := a.BatteryReport(root)
	if err != nil || !r.Battery.Available || r.Report != "report for 000A2700137F4C2D" {
		t.Fatalf("report: %+v %v", r, err)
	}
	if v, _ := a.Battery(root); !v.Available {
		t.Fatal("report should refresh the cache")
	}
}
