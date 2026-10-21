// Package app is PodManager's backend: the operations the UI can invoke and
// the long-running jobs (copying, deleting, retagging) with progress events.
package app

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"podmanager/internal/iap"
	"podmanager/internal/ipod"
	"podmanager/internal/itdb"
	"podmanager/internal/media"
)

// Version of the application.
const Version = "1.4.2"

// Platform abstracts the operating system services the app needs.
type Platform interface {
	Roots() []string
	VolumeInfo(root string) (label string, total, free uint64, err error)
	Eject(root string) error
	PickFiles() ([]string, error)
	PickFolder(title string) (string, error)
	Reveal(path string) error
	DragOut(files []DragFile) error
	// Battery asks the iPod whose USB serial is serial (its FireWire GUID;
	// empty if unknown) for its power state.
	Battery(serial string) (iap.Battery, error)
	// BatteryReport does the same while writing down every step, for
	// troubleshooting.
	BatteryReport(serial string) (iap.Battery, string, error)
}

// Event is pushed to the UI.
type Event struct {
	Type    string       `json:"type"` // progress, done, devices, deviceGone, log
	Job     string       `json:"job,omitempty"`
	Done    int          `json:"done,omitempty"`
	Total   int          `json:"total,omitempty"`
	Label   string       `json:"label,omitempty"`
	Message string       `json:"message,omitempty"`
	Result  *JobResult   `json:"result,omitempty"`
	Devices []DeviceInfo `json:"devices,omitempty"`
	Drop    *DropCheck   `json:"drop,omitempty"`
}

// JobResult summarises a finished job.
type JobResult struct {
	Job       string     `json:"job"`
	OK        int        `json:"ok"`
	Skipped   int        `json:"skipped"`
	Cancelled bool       `json:"cancelled"`
	Errors    []JobError `json:"errors"`
	Notes     []string   `json:"notes"`
	Elapsed   float64    `json:"elapsed"`
	Error     string     `json:"error,omitempty"` // fatal error
}

// JobError is one per-file failure.
type JobError struct {
	Item    string `json:"item"`
	Message string `json:"message"`
}

// DeviceInfo describes a connected iPod.
type DeviceInfo struct {
	Root     string     `json:"root"`
	Name     string     `json:"name"`
	Model    ipod.Model `json:"model"`
	Firmware string     `json:"firmware"`
	HasDB    bool       `json:"hasDB"`
	Total    uint64     `json:"total"`
	Free     uint64     `json:"free"`
	Open     bool       `json:"open"`
}

// TrackView is a song as shown in the UI.
type TrackView struct {
	ID          uint32 `json:"id"`
	Title       string `json:"title"`
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Composer    string `json:"composer,omitempty"`
	Comment     string `json:"comment,omitempty"`
	Grouping    string `json:"grouping,omitempty"`
	Year        uint32 `json:"year,omitempty"`
	TrackNumber uint32 `json:"trackNumber,omitempty"`
	TrackCount  uint32 `json:"trackCount,omitempty"`
	DiscNumber  uint32 `json:"discNumber,omitempty"`
	DiscCount   uint32 `json:"discCount,omitempty"`
	BPM         uint16 `json:"bpm,omitempty"`
	Compilation bool   `json:"compilation,omitempty"`
	Rating      uint8  `json:"rating,omitempty"` // 0..100
	Plays       uint32 `json:"plays,omitempty"`
	Skips       uint32 `json:"skips,omitempty"`
	LengthMs    uint32 `json:"lengthMs"`
	Size        uint32 `json:"size,omitempty"`
	Bitrate     uint32 `json:"bitrate,omitempty"`
	SampleRate  uint32 `json:"sampleRate,omitempty"`
	Kind        string `json:"kind,omitempty"`
	DateAdded   int64  `json:"dateAdded,omitempty"`
	LastPlayed  int64  `json:"lastPlayed,omitempty"`
	Path        string `json:"path,omitempty"`
	MediaType   uint32 `json:"mediaType,omitempty"`
}

// PlaylistView is a playlist as shown in the UI.
type PlaylistView struct {
	Name     string   `json:"name"`
	Smart    bool     `json:"smart"`
	Podcast  bool     `json:"podcast"`
	TrackIDs []uint32 `json:"trackIds"`
}

// Library is the full state of the open iPod.
type Library struct {
	Device    DeviceInfo     `json:"device"`
	Tracks    []TrackView    `json:"tracks"`
	Playlists []PlaylistView `json:"playlists"`
	ReadOnly  string         `json:"readOnly"`
	Music     uint64         `json:"musicBytes"`
}

// App holds the backend state.
type App struct {
	plat      Platform
	emit      func(Event)
	backupDir string

	mu        sync.Mutex
	dev       *ipod.Device
	busy      bool
	cancel    atomic.Bool
	lastRoots string
	prefsPath string
	zoomFn    func(float64)
	batt      batteryCache
}

// New creates the app. emit is called (from any goroutine) to push events.
func New(p Platform, backupDir string, emit func(Event)) *App {
	return &App{plat: p, emit: emit, backupDir: backupDir}
}

// Methods returns the operations callable from the UI, keyed by name.
func (a *App) Methods() map[string]interface{} {
	return map[string]interface{}{
		"version":       func() string { return Version },
		"devices":       a.Devices,
		"open":          a.Open,
		"library":       a.Library,
		"initialize":    a.Initialize,
		"addFiles":      a.AddFiles,
		"addFolder":     a.AddFolder,
		"addPaths":      a.AddPaths,
		"remove":        a.Remove,
		"update":        a.Update,
		"export":        a.Export,
		"cancel":        a.Cancel,
		"eject":         a.Eject,
		"rename":        a.Rename,
		"reveal":        a.Reveal,
		"close":         a.Close,
		"prefs":         a.Prefs,
		"artwork":       a.Artwork,
		"dragOut":       a.DragOut,
		"battery":       a.Battery,
		"batteryReport": a.BatteryReport,
		"setScale":      a.SetScale,
		"setPref":       a.SetPref,
	}
}

// ------------------------------------------------------------- devices

// Devices lists connected iPods.
func (a *App) Devices() ([]DeviceInfo, error) {
	var out []DeviceInfo
	for _, r := range a.plat.Roots() {
		if !ipod.LooksLikeIPod(r) {
			continue
		}
		out = append(out, a.deviceInfo(r))
	}
	return out, nil
}

func (a *App) deviceInfo(root string) DeviceInfo {
	label, total, free, _ := a.plat.VolumeInfo(root)
	m, fw := ipod.ReadModel(root)
	di := DeviceInfo{Root: root, Name: label, Model: m, Firmware: fw, HasDB: ipod.HasDatabase(root), Total: total, Free: free}
	a.mu.Lock()
	if a.dev != nil && sameRoot(a.dev.Root, root) {
		di.Open = true
		di.Name = a.dev.Name()
	}
	a.mu.Unlock()
	if di.Name == "" {
		di.Name = "iPod"
	}
	return di
}

func sameRoot(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }

// Watch polls for iPods being connected or removed and emits events.
func (a *App) Watch(stop <-chan struct{}) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		var roots []string
		for _, r := range a.plat.Roots() {
			if ipod.LooksLikeIPod(r) {
				roots = append(roots, r)
			}
		}
		key := strings.Join(roots, "|")
		a.mu.Lock()
		changed := key != a.lastRoots
		a.lastRoots = key
		var gone bool
		if a.dev != nil && !a.busy && !ipod.LooksLikeIPod(a.dev.Root) {
			a.dev = nil
			gone = true
		}
		a.mu.Unlock()
		if gone {
			a.emit(Event{Type: "deviceGone", Message: "The iPod was disconnected."})
		}
		if changed {
			devs, _ := a.Devices()
			a.emit(Event{Type: "devices", Devices: devs})
		}
	}
}

// Open opens an iPod and returns its library.
func (a *App) Open(root string) (*Library, error) {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return nil, errors.New("please wait for the current task to finish")
	}
	a.mu.Unlock()
	if !ipod.LooksLikeIPod(root) {
		return nil, fmt.Errorf("%s doesn't look like an iPod", root)
	}
	d, err := ipod.Open(root, a.backupDir)
	if errors.Is(err, ipod.ErrNoDatabase) {
		return nil, fmt.Errorf("NO_DATABASE: %v", err)
	}
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.dev = d
	a.mu.Unlock()
	return a.Library()
}

// Close forgets the open iPod.
func (a *App) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.busy {
		return errors.New("please wait for the current task to finish")
	}
	a.dev = nil
	return nil
}

// Initialize creates an empty music database on an iPod that has none.
func (a *App) Initialize(root, name string) (*Library, error) {
	if strings.TrimSpace(name) == "" {
		name = "iPod"
	}
	if ipod.HasDatabase(root) {
		return nil, errors.New("this iPod already has a music database")
	}
	d, err := ipod.Initialize(root, strings.TrimSpace(name), a.backupDir)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.dev = d
	a.mu.Unlock()
	return a.Library()
}

// Library returns the open iPod's contents.
func (a *App) Library() (*Library, error) {
	a.mu.Lock()
	d := a.dev
	a.mu.Unlock()
	if d == nil {
		return nil, errors.New("no iPod is open")
	}
	info := a.deviceInfo(d.Root)
	a.mu.Lock()
	defer a.mu.Unlock()
	lib := &Library{Device: info, ReadOnly: d.ReadOnly, Music: d.MusicBytes(), Tracks: make([]TrackView, 0, len(d.DB.Tracks)), Playlists: []PlaylistView{}}
	for _, t := range d.DB.Tracks {
		lib.Tracks = append(lib.Tracks, view(t))
	}
	for i, p := range d.DB.Playlists {
		if i == 0 {
			continue
		}
		pv := PlaylistView{Name: p.Name, Smart: p.IsSmart(), Podcast: p.IsPodcast(), TrackIDs: make([]uint32, 0, len(p.Items))}
		for _, it := range p.Items {
			pv.TrackIDs = append(pv.TrackIDs, it.TrackID)
		}
		lib.Playlists = append(lib.Playlists, pv)
	}
	return lib, nil
}

func unixOf(mac uint32) int64 {
	if mac == 0 {
		return 0
	}
	return itdb.FromMacTime(mac).Unix()
}

func view(t *itdb.Track) TrackView {
	return TrackView{
		ID: t.ID(), Title: t.Title(), Artist: t.Artist(), Album: t.Album(), AlbumArtist: t.AlbumArtist(),
		Genre: t.Genre(), Composer: t.Composer(), Comment: t.Comment(), Grouping: t.Grouping(),
		Year: t.Year(), TrackNumber: t.TrackNumber(), TrackCount: t.TrackCount(), DiscNumber: t.DiscNumber(),
		DiscCount: t.DiscCount(), BPM: t.BPM(), Compilation: t.Compilation(), Rating: t.Rating(),
		Plays: t.PlayCount(), Skips: t.SkipCount(), LengthMs: t.LengthMs(), Size: t.Size(), Bitrate: t.Bitrate(),
		SampleRate: t.SampleRate(), Kind: t.Kind(), DateAdded: unixOf(t.DateAdded()), LastPlayed: unixOf(t.LastPlayed()),
		Path: t.RelPath(), MediaType: t.MediaType(),
	}
}

// Rename changes the iPod's name.
func (a *App) Rename(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("the name can't be empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dev == nil {
		return errors.New("no iPod is open")
	}
	if a.busy {
		return errors.New("please wait for the current task to finish")
	}
	old := a.dev.DB.Master().Name
	a.dev.DB.Master().Name = name
	if err := a.dev.Save(); err != nil {
		a.dev.DB.Master().Name = old
		return err
	}
	return nil
}

// Eject safely ejects the open iPod (or the given root).
func (a *App) Eject(root string) error {
	a.mu.Lock()
	if a.busy {
		a.mu.Unlock()
		return errors.New("please wait for the current task to finish before ejecting")
	}
	if root == "" && a.dev != nil {
		root = a.dev.Root
	}
	if a.dev != nil && sameRoot(a.dev.Root, root) {
		a.dev = nil
	}
	a.mu.Unlock()
	if root == "" {
		return errors.New("no iPod selected")
	}
	return a.plat.Eject(root)
}

// Reveal opens Explorer at a song's file.
func (a *App) Reveal(id uint32) error {
	a.mu.Lock()
	d := a.dev
	a.mu.Unlock()
	if d == nil {
		return errors.New("no iPod is open")
	}
	t := d.DB.TrackByID(id)
	if t == nil {
		return errors.New("song not found")
	}
	return a.plat.Reveal(d.AbsPath(t))
}

// Cancel asks the running job to stop after the current item.
func (a *App) Cancel() { a.cancel.Store(true) }

// ---------------------------------------------------------------- jobs

func (a *App) startJob(name string, allowReadOnly bool, fn func(d *ipod.Device, res *JobResult) error) error {
	a.mu.Lock()
	if a.dev == nil {
		a.mu.Unlock()
		return errors.New("no iPod is open")
	}
	if a.busy {
		a.mu.Unlock()
		return errors.New("another task is still running")
	}
	if a.dev.ReadOnly != "" && !allowReadOnly {
		a.mu.Unlock()
		return errors.New(a.dev.ReadOnly)
	}
	a.busy = true
	a.cancel.Store(false)
	d := a.dev
	a.mu.Unlock()
	go func() {
		start := time.Now()
		res := &JobResult{Job: name, Errors: []JobError{}, Notes: []string{}}
		func() {
			defer func() {
				if r := recover(); r != nil {
					res.Error = fmt.Sprint("internal error: ", r)
				}
			}()
			if err := fn(d, res); err != nil {
				res.Error = err.Error()
			}
		}()
		res.Cancelled = a.cancel.Load()
		res.Elapsed = time.Since(start).Seconds()
		a.mu.Lock()
		a.busy = false
		a.mu.Unlock()
		a.emit(Event{Type: "done", Job: name, Result: res})
	}()
	return nil
}

func (a *App) progress(job string, done, total int, label string) {
	a.emit(Event{Type: "progress", Job: job, Done: done, Total: total, Label: label})
}

// withDB runs fn while holding the lock (so Library() never sees a half
// updated database).
func (a *App) withDB(fn func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	fn()
}

// AddFiles lets the user pick songs and copies them to the iPod.
func (a *App) AddFiles() (bool, error) {
	paths, err := a.plat.PickFiles()
	if err != nil || len(paths) == 0 {
		return false, err
	}
	return true, a.AddPaths(paths)
}

// AddFolder lets the user pick a folder; every song inside it (including
// sub-folders) is copied to the iPod.
func (a *App) AddFolder() (bool, error) {
	dir, err := a.plat.PickFolder("Choose a folder of music to add to your iPod")
	if err != nil || dir == "" {
		return false, err
	}
	return true, a.AddPaths([]string{dir})
}

type dupKey struct{ title, artist, album string }

func normKey(title, artist, album string) dupKey {
	f := func(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
	return dupKey{f(title), f(artist), f(album)}
}

// AddPaths copies the given files / folders to the iPod.
func (a *App) AddPaths(paths []string) error {
	return a.startJob("add", false, func(d *ipod.Device, res *JobResult) error {
		a.progress("add", 0, 0, "Looking for songs…")
		var files []string
		for _, p := range paths {
			st, err := os.Stat(p)
			if err != nil {
				res.Errors = append(res.Errors, JobError{p, err.Error()})
				continue
			}
			if !st.IsDir() {
				files = append(files, p)
				continue
			}
			filepath.WalkDir(p, func(path string, e fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if a.cancel.Load() {
					return filepath.SkipAll
				}
				name := e.Name()
				if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "._") {
					if e.IsDir() {
						return filepath.SkipDir
					}
					return nil
				}
				if !e.IsDir() && media.KnownAudioExt(path) {
					files = append(files, path)
				}
				return nil
			})
		}
		sort.Strings(files)
		if len(files) == 0 {
			res.Notes = append(res.Notes, "No songs were found.")
			return nil
		}
		existing := map[dupKey][]uint32{}
		a.withDB(func() {
			for _, t := range d.DB.Tracks {
				k := normKey(t.Title(), t.Artist(), t.Album())
				existing[k] = append(existing[k], t.LengthMs())
			}
		})
		_, _, free, _ := a.plat.VolumeInfo(d.Root)
		const reserve = 16 << 20
		sinceSave := 0
		for i, f := range files {
			if a.cancel.Load() {
				break
			}
			a.progress("add", i, len(files), filepath.Base(f))
			info, err := media.Probe(f)
			if err != nil {
				res.Errors = append(res.Errors, JobError{f, err.Error()})
				continue
			}
			k := normKey(info.Tags.Title, info.Tags.Artist, info.Tags.Album)
			dup := false
			for _, l := range existing[k] {
				diff := int64(l) - int64(info.LengthMs)
				if diff < 2000 && diff > -2000 {
					dup = true
				}
			}
			if dup {
				res.Skipped++
				continue
			}
			if free > 0 && uint64(info.Size)+reserve > free {
				res.Errors = append(res.Errors, JobError{f, "the iPod is full"})
				res.Notes = append(res.Notes, "Stopped because the iPod ran out of space.")
				break
			}
			abs, loc, err := d.AllocateFile(info.Ext)
			if err != nil {
				return err
			}
			if err := copyFile(f, abs); err != nil {
				os.Remove(abs)
				res.Errors = append(res.Errors, JobError{f, "copy failed: " + err.Error()})
				if errors.Is(err, fs.ErrPermission) || isDiskFull(err) {
					break
				}
				continue
			}
			if free > uint64(info.Size) {
				free -= uint64(info.Size)
			}
			tg := info.Tags
			mt := uint32(1)
			if info.Audiobook {
				mt = 8
			}
			a.withDB(func() {
				d.DB.NewTrack(itdb.NewTrackInfo{
					Location: loc, FileTypeCode: info.FileTypeCode, Kind: info.Kind, Size: uint32(info.Size),
					LengthMs: info.LengthMs, Bitrate: info.Bitrate, SampleRate: info.SampleRate, VBR: info.VBR,
					IsAAC: info.Format != "mp3", SampleCount: info.SampleCount, MediaType: mt, RememberPos: info.Audiobook,
					Title: tg.Title, Artist: tg.Artist, Album: tg.Album, AlbumArtist: tg.AlbumArtist, Genre: tg.Genre,
					Composer: tg.Composer, Comment: tg.Comment, Grouping: tg.Grouping,
					TrackNumber: uint32(clampU(tg.Track)), TrackCount: uint32(clampU(tg.TrackTotal)),
					DiscNumber: uint32(clampU(tg.Disc)), DiscCount: uint32(clampU(tg.DiscTotal)), Year: uint32(clampU(tg.Year)),
					BPM: uint16(clampU(tg.BPM)), Compilation: tg.Compilation,
				})
			})
			existing[k] = append(existing[k], info.LengthMs)
			res.OK++
			sinceSave++
			if sinceSave >= 50 {
				var err error
				a.withDB(func() { err = d.Save() })
				if err != nil {
					return fmt.Errorf("could not update the iPod database: %w", err)
				}
				sinceSave = 0
			}
		}
		a.progress("add", len(files), len(files), "Updating iPod database…")
		var err error
		a.withDB(func() { err = d.Save() })
		if err != nil {
			return fmt.Errorf("could not update the iPod database: %w", err)
		}
		if res.Skipped > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("%d song(s) were already on the iPod and were skipped.", res.Skipped))
		}
		return nil
	})
}

func clampU(v int) int {
	if v < 0 {
		return 0
	}
	if v > 65535 {
		return 65535
	}
	return v
}

func isDiskFull(err error) bool {
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "no space") || strings.Contains(s, "not enough space") || strings.Contains(s, "disk is full")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(out, in, make([]byte, 1<<20)); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Remove deletes songs from the iPod.
func (a *App) Remove(ids []uint32) error {
	if len(ids) == 0 {
		return errors.New("no songs selected")
	}
	return a.startJob("remove", false, func(d *ipod.Device, res *JobResult) error {
		var removed []uint32
		for i, id := range ids {
			if a.cancel.Load() {
				break
			}
			var t *itdb.Track
			a.withDB(func() { t = d.DB.TrackByID(id) })
			if t == nil {
				continue
			}
			a.progress("remove", i, len(ids), t.Title())
			p := d.AbsPath(t)
			if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
				res.Errors = append(res.Errors, JobError{t.Title(), err.Error()})
				continue
			}
			removed = append(removed, id)
			res.OK++
		}
		a.progress("remove", len(ids), len(ids), "Updating iPod database…")
		var err error
		a.withDB(func() {
			d.DB.RemoveTracks(removed)
			err = d.Save()
		})
		return err
	})
}

// Patch is a partial metadata update; nil fields are left unchanged.
type Patch struct {
	Title       *string `json:"title"`
	Artist      *string `json:"artist"`
	Album       *string `json:"album"`
	AlbumArtist *string `json:"albumArtist"`
	Genre       *string `json:"genre"`
	Composer    *string `json:"composer"`
	Comment     *string `json:"comment"`
	Grouping    *string `json:"grouping"`
	Year        *int    `json:"year"`
	TrackNumber *int    `json:"trackNumber"`
	TrackCount  *int    `json:"trackCount"`
	DiscNumber  *int    `json:"discNumber"`
	DiscCount   *int    `json:"discCount"`
	BPM         *int    `json:"bpm"`
	Rating      *int    `json:"rating"`
	Compilation *bool   `json:"compilation"`
}

func (p *Patch) touchesFile() bool {
	return p.Title != nil || p.Artist != nil || p.Album != nil || p.AlbumArtist != nil || p.Genre != nil ||
		p.Composer != nil || p.Comment != nil || p.Grouping != nil || p.Year != nil || p.TrackNumber != nil ||
		p.TrackCount != nil || p.DiscNumber != nil || p.DiscCount != nil || p.BPM != nil || p.Compilation != nil
}

func applyPatch(t *itdb.Track, p *Patch) {
	str := func(typ, sortTyp uint32, v *string) {
		if v == nil {
			return
		}
		s := strings.TrimSpace(*v)
		if s != t.Str(typ) {
			t.SetStr(typ, s)
			if sortTyp != 0 {
				t.SetStr(sortTyp, "") // stale sort key
			}
		}
	}
	str(itdb.MhodTitle, itdb.MhodSortTitle, p.Title)
	str(itdb.MhodArtist, itdb.MhodSortArtist, p.Artist)
	str(itdb.MhodAlbum, itdb.MhodSortAlbum, p.Album)
	str(itdb.MhodAlbumArtist, itdb.MhodSortAlbumArtist, p.AlbumArtist)
	str(itdb.MhodGenre, 0, p.Genre)
	str(itdb.MhodComposer, itdb.MhodSortComposer, p.Composer)
	str(itdb.MhodComment, 0, p.Comment)
	str(itdb.MhodGrouping, 0, p.Grouping)
	num := func(v *int, set func(uint32)) {
		if v != nil {
			set(uint32(clampU(*v)))
		}
	}
	num(p.Year, t.SetYear)
	num(p.TrackNumber, t.SetTrackNumber)
	num(p.TrackCount, t.SetTrackCount)
	num(p.DiscNumber, t.SetDiscNumber)
	num(p.DiscCount, t.SetDiscCount)
	if p.BPM != nil {
		t.SetBPM(uint16(clampU(*p.BPM)))
	}
	if p.Rating != nil {
		r := *p.Rating
		if r < 0 {
			r = 0
		}
		if r > 100 {
			r = 100
		}
		t.SetRating(uint8(r / 20 * 20))
	}
	if p.Compilation != nil {
		t.SetCompilation(*p.Compilation)
	}
	t.SetTimeModified(itdb.MacTime(time.Now()))
}

func tagsOf(t *itdb.Track) media.Tags {
	return media.Tags{
		Title: t.Title(), Artist: t.Artist(), Album: t.Album(), AlbumArtist: t.AlbumArtist(), Genre: t.Genre(),
		Composer: t.Composer(), Comment: t.Comment(), Grouping: t.Grouping(), Year: int(t.Year()),
		Track: int(t.TrackNumber()), TrackTotal: int(t.TrackCount()), Disc: int(t.DiscNumber()),
		DiscTotal: int(t.DiscCount()), BPM: int(t.BPM()), Compilation: t.Compilation(),
	}
}

// Update edits the metadata of one or more songs. When writeTags is set the
// song files on the iPod are retagged too.
func (a *App) Update(ids []uint32, patch Patch, writeTags bool) error {
	if len(ids) == 0 {
		return errors.New("no songs selected")
	}
	return a.startJob("update", false, func(d *ipod.Device, res *JobResult) error {
		retag := writeTags && patch.touchesFile()
		noTagFormats := 0
		for i, id := range ids {
			if a.cancel.Load() {
				break
			}
			var t *itdb.Track
			var tags media.Tags
			a.withDB(func() {
				t = d.DB.TrackByID(id)
				if t != nil {
					applyPatch(t, &patch)
					tags = tagsOf(t)
				}
			})
			if t == nil {
				continue
			}
			a.progress("update", i, len(ids), tags.Title)
			res.OK++
			if !retag {
				continue
			}
			p := d.AbsPath(t)
			if err := media.WriteTags(p, tags); err != nil {
				if media.IsNoTagSupport(err) {
					noTagFormats++
				} else {
					res.Errors = append(res.Errors, JobError{tags.Title, "couldn't update the file's tags: " + err.Error()})
				}
				continue
			}
			if st, err := os.Stat(p); err == nil {
				a.withDB(func() { t.SetSize(uint32(st.Size())) })
			}
		}
		if noTagFormats > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("%d WAV/AIFF song(s) can't store tags in the file; the iPod database was updated.", noTagFormats))
		}
		a.progress("update", len(ids), len(ids), "Updating iPod database…")
		var err error
		a.withDB(func() { err = d.Save() })
		return err
	})
}

// Export copies songs from the iPod to a folder on the computer, organised
// as Artist/Album/NN Title.ext.
func (a *App) Export(ids []uint32) (bool, error) {
	if len(ids) == 0 {
		return false, errors.New("no songs selected")
	}
	dir, err := a.plat.PickFolder("Choose where to copy the songs")
	if err != nil || dir == "" {
		return false, err
	}
	err = a.startJob("export", true, func(d *ipod.Device, res *JobResult) error {
		for i, id := range ids {
			if a.cancel.Load() {
				break
			}
			var t *itdb.Track
			a.withDB(func() { t = d.DB.TrackByID(id) })
			if t == nil {
				continue
			}
			a.progress("export", i, len(ids), t.Title())
			artist := firstNonEmpty(t.AlbumArtist(), t.Artist(), "Unknown Artist")
			if t.Compilation() {
				artist = "Compilations"
			}
			album := firstNonEmpty(t.Album(), "Unknown Album")
			name := sanitize(t.Title())
			if t.TrackNumber() > 0 {
				name = fmt.Sprintf("%02d %s", t.TrackNumber(), name)
			}
			src := d.AbsPath(t)
			ext := strings.ToLower(filepath.Ext(src))
			target := filepath.Join(dir, sanitize(artist), sanitize(album))
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			dst := filepath.Join(target, name+ext)
			for n := 2; ; n++ {
				if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
					break
				}
				dst = filepath.Join(target, fmt.Sprintf("%s (%d)%s", name, n, ext))
			}
			if err := copyFile(src, dst); err != nil {
				os.Remove(dst)
				res.Errors = append(res.Errors, JobError{t.Title(), err.Error()})
				continue
			}
			res.OK++
		}
		res.Notes = append(res.Notes, "Saved to "+dir)
		return nil
	})
	return err == nil, err
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) || r < 32 {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	s = strings.TrimRight(s, ". ")
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:80])
	}
	if s == "" {
		s = "Untitled"
	}
	return s
}
