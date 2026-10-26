// Package app is Classic iPod Manager's backend: the operations the UI can invoke and
// the long-running jobs (copying, deleting, retagging) with progress events.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
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
const Version = "1.8.0"

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
	// PickAny shows an Open dialog with the given file types.
	PickAny(title string, filters []FileFilter, multi bool) ([]string, error)
	// OpenFolder shows a folder in Explorer.
	OpenFolder(path string) error
}

// FileFilter is one entry of an Open dialog's file type list.
type FileFilter struct {
	Name    string // "Images"
	Pattern string // "*.jpg;*.png"
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
	// file transfers: bytes copied so far, expected total (0 = unknown),
	// bytes per second over the last few seconds, "to" or "from" the iPod
	BytesDone  int64   `json:"bytesDone,omitempty"`
	BytesTotal int64   `json:"bytesTotal,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	Direction  string  `json:"direction,omitempty"`
}

// JobResult summarises a finished job.
type JobResult struct {
	Job       string      `json:"job"`
	OK        int         `json:"ok"`
	Skipped   int         `json:"skipped"`
	Cancelled bool        `json:"cancelled"`
	Errors    []JobError  `json:"errors"`
	Notes     []string    `json:"notes"`
	Elapsed   float64     `json:"elapsed"`
	Error     string      `json:"error,omitempty"` // fatal error
	FM        []FMOutcome `json:"fm,omitempty"`    // Format Manager: what happened to each song
	Videos    int         `json:"videos"`          // videos added
	Converted int         `json:"converted"`       // videos converted while adding
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
	Video       bool   `json:"video,omitempty"`
}

// PlaylistView is a playlist as shown in the UI.
type PlaylistView struct {
	ID       string   `json:"id"` // decimal; JavaScript numbers can't hold it
	Editable bool     `json:"editable"`
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
	Video     uint64         `json:"videoBytes"`
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
	mainDir   string
	stopFn    context.CancelFunc // cancels the running job's external programs
	xfer      *transfer          // byte counting for the running job
	play      player
}

// New creates the app. emit is called (from any goroutine) to push events.
func New(p Platform, backupDir string, emit func(Event)) *App {
	return &App{plat: p, emit: emit, backupDir: backupDir}
}

// Methods returns the operations callable from the UI, keyed by name.
func (a *App) Methods() map[string]interface{} {
	return map[string]interface{}{
		"version":            func() string { return Version },
		"devices":            a.Devices,
		"open":               a.Open,
		"library":            a.Library,
		"initialize":         a.Initialize,
		"addFiles":           a.AddFiles,
		"addFolder":          a.AddFolder,
		"addPaths":           a.AddPaths,
		"remove":             a.Remove,
		"update":             a.Update,
		"export":             a.Export,
		"cancel":             a.Cancel,
		"eject":              a.Eject,
		"rename":             a.Rename,
		"reveal":             a.Reveal,
		"close":              a.Close,
		"prefs":              a.Prefs,
		"artwork":            a.Artwork,
		"dragOut":            a.DragOut,
		"battery":            a.Battery,
		"encoders":           a.Encoders,
		"setEncoder":         a.SetEncoder,
		"openFolder":         a.OpenFolder,
		"fmInspect":          a.FMInspect,
		"fmFromIPod":         a.FMFromIPod,
		"fmPickFiles":        a.FMPickFiles,
		"fmPickFolder":       a.FMPickFolder,
		"fmPickImage":        a.FMPickImage,
		"fmImagePreview":     a.FMImagePreview,
		"fmArtwork":          a.FMArtwork,
		"fmRun":              a.FMRun,
		"addVideos":          a.AddVideos,
		"addFolderOf":        a.AddFolderOf,
		"addMedia":           a.AddMedia,
		"createPlaylist":     a.CreatePlaylist,
		"mediaInfo":          a.MediaInfo,
		"renamePlaylist":     a.RenamePlaylist,
		"deletePlaylist":     a.DeletePlaylist,
		"addToPlaylist":      a.AddToPlaylist,
		"removeFromPlaylist": a.RemoveFromPlaylist,
		"movePlaylistItems":  a.MovePlaylistItems,
		"revealFile":         a.RevealFile,
		"batteryReport":      a.BatteryReport,
		"setScale":           a.SetScale,
		"setPref":            a.SetPref,
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
	lib := &Library{Device: info, ReadOnly: d.ReadOnly, Music: d.MusicBytes(), Video: d.VideoBytes(), Tracks: make([]TrackView, 0, len(d.DB.Tracks)), Playlists: []PlaylistView{}}
	for _, t := range d.DB.Tracks {
		lib.Tracks = append(lib.Tracks, view(t))
	}
	for i, p := range d.DB.Playlists {
		if i == 0 {
			continue
		}
		pv := PlaylistView{ID: strconv.FormatUint(p.ID(), 10), Editable: p.Editable(), Name: p.Name, Smart: p.IsSmart(), Podcast: p.IsPodcast(), TrackIDs: make([]uint32, 0, len(p.Items))}
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
		Video: t.IsVideo(), Plays: t.PlayCount(), Skips: t.SkipCount(), LengthMs: t.LengthMs(), Size: t.Size(), Bitrate: t.Bitrate(),
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
func (a *App) Cancel() {
	a.cancel.Store(true)
	a.mu.Lock()
	if a.stopFn != nil {
		a.stopFn()
	}
	a.mu.Unlock()
}

// ---------------------------------------------------------------- jobs

func (a *App) startJob(name string, allowReadOnly bool, fn func(d *ipod.Device, res *JobResult) error) error {
	return a.startTask(name, allowReadOnly, true, func(ctx context.Context, d *ipod.Device, res *JobResult) error {
		return fn(d, res)
	})
}

// startTask runs a background job. With needDev false it also runs when no
// iPod is open (d is then nil). ctx is cancelled by Cancel.
func (a *App) startTask(name string, allowReadOnly, needDev bool, fn func(ctx context.Context, d *ipod.Device, res *JobResult) error) error {
	a.mu.Lock()
	if a.dev == nil && needDev {
		a.mu.Unlock()
		return errors.New("no iPod is open")
	}
	if a.busy {
		a.mu.Unlock()
		return errors.New("another task is still running")
	}
	if needDev && a.dev.ReadOnly != "" && !allowReadOnly {
		a.mu.Unlock()
		return errors.New(a.dev.ReadOnly)
	}
	a.busy = true
	a.cancel.Store(false)
	d := a.dev
	ctx, stop := context.WithCancel(context.Background())
	a.stopFn = stop
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
			if err := fn(ctx, d, res); err != nil {
				res.Error = err.Error()
			}
		}()
		res.Cancelled = a.cancel.Load()
		res.Elapsed = time.Since(start).Seconds()
		a.mu.Lock()
		a.busy = false
		a.stopFn = nil
		a.xfer = nil
		a.mu.Unlock()
		stop()
		a.emit(Event{Type: "done", Job: name, Result: res})
	}()
	return nil
}

func (a *App) progress(job string, done, total int, label string) {
	if x := a.currentTransfer(); x != nil && x.job == job {
		x.mu.Lock()
		x.done, x.total, x.label = done, total, label
		e := x.event(time.Now())
		x.mu.Unlock()
		a.emit(e)
		return
	}
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

// AddPaths copies the given files / folders (songs and videos) to the iPod.
func (a *App) AddPaths(paths []string) error { return a.AddMedia(paths, "", 0) }

// AddMedia copies files / folders to the iPod. kind limits what is picked
// up inside folders: "music", "video" or "" for both. Files named directly
// are always added. videoType files videos under Movies (2), Music Videos
// (32) or TV Shows (64); 0 uses the kind stored in the file, or Movies.
func (a *App) AddMedia(paths []string, kind string, videoType int) error {
	return a.startTask("add", false, true, func(ctx context.Context, d *ipod.Device, res *JobResult) error {
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
				if e.IsDir() {
					return nil
				}
				// .mp4 can be a song or a video: look inside
				isVid := media.VideoExt(path) && (!media.KnownAudioExt(path) || media.IsVideo(path))
				switch {
				case isVid && kind != "music":
					files = append(files, path)
				case !isVid && media.KnownAudioExt(path) && kind != "video":
					files = append(files, path)
				}
				return nil
			})
		}
		sort.Strings(files)
		if len(files) == 0 {
			if kind == "video" {
				res.Notes = append(res.Notes, "No videos were found.")
			} else {
				res.Notes = append(res.Notes, "No songs were found.")
			}
			return nil
		}
		return a.addFiles(ctx, d, "add", files, uint32(videoType), res)
	})
}

// AddVideos lets the user pick videos and copies them to the iPod, filed
// under videoType (see AddMedia).
func (a *App) AddVideos(videoType int) (bool, error) {
	paths, err := a.plat.PickAny("Add Videos to iPod", []FileFilter{
		{"Videos", "*.m4v;*.mp4;*.mov;*.avi;*.mkv;*.wmv;*.webm;*.flv;*.mpg;*.mpeg;*.m2ts;*.mts;*.ts;*.3gp;*.divx;*.vob;*.ogv;*.asf"},
		{"All files", "*.*"},
	}, true)
	if err != nil || len(paths) == 0 {
		return false, err
	}
	return true, a.AddMedia(paths, "video", videoType)
}

// AddFolderOf lets the user pick a folder and adds its songs ("music") or
// videos ("video").
func (a *App) AddFolderOf(kind string, videoType int) (bool, error) {
	title := "Choose a folder of music to add to your iPod"
	if kind == "video" {
		title = "Choose a folder of videos to add to your iPod"
	}
	dir, err := a.plat.PickFolder(title)
	if err != nil || dir == "" {
		return false, err
	}
	return true, a.AddMedia([]string{dir}, kind, videoType)
}

// addFiles copies song files to the iPod and adds them to its database,
// skipping songs that are already there. Covers embedded in the files are
// added to the iPod's artwork database.
func (a *App) addFiles(ctx context.Context, d *ipod.Device, job string, files []string, videoType uint32, res *JobResult) error {
	{
		videos, converted := 0, 0
		existing := map[dupKey][]uint32{}
		a.withDB(func() {
			for _, t := range d.DB.Tracks {
				k := normKey(t.Title(), t.Artist(), t.Album())
				existing[k] = append(existing[k], t.LengthMs())
			}
		})
		_, _, free, _ := a.plat.VolumeInfo(d.Root)
		const reserve = 16 << 20
		var planned int64
		for _, f := range files {
			if st, err := os.Stat(f); err == nil {
				planned += st.Size()
			}
		}
		xf := a.currentTransfer()
		if xf == nil || xf.job != job {
			xf = a.startTransfer(job, "to", planned)
		} else {
			xf.mu.Lock()
			xf.total64 += planned
			xf.mu.Unlock()
		}
		sinceSave := 0
		for i, f := range files {
			if a.cancel.Load() {
				break
			}
			a.progress(job, i, len(files), filepath.Base(f))
			src := f // the file copied to the iPod
			var info *media.Info
			var video *media.VideoInfo
			if media.IsVideo(f) {
				vi, err := media.ProbeVideo(f)
				if err != nil {
					res.Errors = append(res.Errors, JobError{f, err.Error()})
					continue
				}
				if vi.LengthMs == 0 {
					vi.LengthMs = a.encoderConfig().VideoLength(ctx, f)
				}
				video, info = vi, videoInfo(vi)
			} else {
				var err error
				if info, err = media.Probe(f); err != nil {
					res.Errors = append(res.Errors, JobError{f, err.Error()})
					continue
				}
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
			if video != nil && !video.Playable {
				out, err := a.convertVideo(ctx, f, video, job, i, len(files))
				if err != nil {
					if ctx.Err() != nil {
						break
					}
					res.Errors = append(res.Errors, JobError{f, err.Error()})
					continue
				}
				cv, err := media.ProbeVideo(out)
				if err != nil {
					res.Errors = append(res.Errors, JobError{f, err.Error()})
					continue
				}
				cv.Tags = video.Tags
				src, video, info = out, cv, videoInfo(cv)
				converted++
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
			if err := copyFileCounted(src, abs, xf); err != nil {
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
			if video != nil {
				mt = videoMediaType(videoType, f)
				videos++
			}
			var nt *itdb.Track
			a.withDB(func() {
				nt = d.DB.NewTrack(itdb.NewTrackInfo{
					Location: loc, FileTypeCode: info.FileTypeCode, Kind: info.Kind, Size: uint32(info.Size),
					LengthMs: info.LengthMs, Bitrate: info.Bitrate, SampleRate: info.SampleRate, VBR: info.VBR,
					IsAAC: info.Format != "mp3", SampleCount: info.SampleCount, MediaType: mt, RememberPos: info.Audiobook,
					Title: tg.Title, Artist: tg.Artist, Album: tg.Album, AlbumArtist: tg.AlbumArtist, Genre: tg.Genre,
					Composer: tg.Composer, Comment: tg.Comment, Grouping: tg.Grouping,
					TrackNumber: uint32(clampU(tg.Track)), TrackCount: uint32(clampU(tg.TrackTotal)),
					DiscNumber: uint32(clampU(tg.Disc)), DiscCount: uint32(clampU(tg.DiscTotal)), Year: uint32(clampU(tg.Year)),
					BPM: uint16(clampU(tg.BPM)), Compilation: tg.Compilation, Movie: video != nil,
				})
			})
			if mt == itdb.MediaTVShow {
				a.withDB(func() { nt.SetStr(itdb.MhodTVShow, firstNonEmpty(tg.Album, tg.Title)) })
			}
			a.addCover(d, nt, src, res)
			if video != nil && d.CanWriteArtwork() {
				var has bool
				a.withDB(func() { has = nt.HasArtwork() })
				if !has {
					if jpg := a.encoderConfig().VideoThumbnail(ctx, src, info.LengthMs); jpg != nil {
						a.withDB(func() { d.SetArtwork(nt, jpg) })
					}
				}
			}
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
		a.progress(job, len(files), len(files), "Updating iPod database…")
		var err error
		a.withDB(func() { err = d.Save() })
		if err != nil {
			return fmt.Errorf("could not update the iPod database: %w", err)
		}
		if res.Skipped > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("%d item(s) were already on the iPod and were skipped.", res.Skipped))
		}
		res.Videos, res.Converted = videos, converted
		if converted > 0 {
			where := ""
			if a.mainDir != "" {
				where = " Copies are in " + filepath.Join(a.mainDir, ConvertedVideosFolder) + "."
			}
			res.Notes = append(res.Notes, fmt.Sprintf("%s converted for the iPod with FFmpeg.%s", plural(converted, "video was", "videos were"), where))
		}
		return nil
	}
}

func plural(n int, one string, many ...string) string {
	if n == 1 {
		return "1 " + one
	}
	if len(many) > 0 {
		return fmt.Sprintf("%d %s", n, many[0])
	}
	return fmt.Sprintf("%d %ss", n, one)
}

// videoMediaType decides where a video is filed: the section it was added
// to, else the kind stored in the file (iTunes' "stik"), else Movies.
func videoMediaType(chosen uint32, path string) uint32 {
	switch chosen {
	case itdb.MediaMovie, itdb.MediaMusicVideo, itdb.MediaTVShow:
		return chosen
	}
	switch media.MP4MediaKind(path) {
	case 6:
		return itdb.MediaMusicVideo
	case 10:
		return itdb.MediaTVShow
	}
	return itdb.MediaMovie
}

// videoInfo describes a video for the iPod database.
func videoInfo(vi *media.VideoInfo) *media.Info {
	sr := vi.SampleRate
	if sr == 0 {
		sr = 44100
	}
	return &media.Info{Format: "video", FileTypeCode: "M4V ", Kind: "MPEG-4 video file", Ext: ".m4v",
		LengthMs: vi.LengthMs, Bitrate: vi.Bitrate, SampleRate: sr, Size: vi.Size, Tags: vi.Tags}
}

// convertVideo makes an iPod-compatible copy of a video with FFmpeg in
// the main folder's Converted Videos folder, reporting progress.
func (a *App) convertVideo(ctx context.Context, src string, vi *media.VideoInfo, job string, n, total int) (string, error) {
	base := filepath.Join(a.mainDir, ConvertedVideosFolder)
	if a.mainDir == "" {
		base = filepath.Join(os.TempDir(), "ClassiciPodManager-videos")
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	var pa pathAllocator
	out := pa.videoPath(base, vi.Tags)
	verb := "Converting"
	if vi.Remux {
		verb = "Re-packaging"
	}
	name := firstNonEmpty(vi.Tags.Title, filepath.Base(src))
	last := -1
	a.progress(job, n, total, fmt.Sprintf("%s %s", verb, name))
	err := a.encoderConfig().ConvertVideo(ctx, src, vi, out, func(f float64) {
		if pct := int(f * 100); pct != last {
			last = pct
			a.progress(job, n, total, fmt.Sprintf("%s %s – %d%%", verb, name, pct))
		}
	})
	if err != nil {
		return "", err
	}
	if err := media.WriteTags(out, vi.Tags); err != nil && !media.IsNoTagSupport(err) {
		return "", fmt.Errorf("couldn't tag the converted video: %w", err)
	}
	return out, nil
}

// addCover puts a song file's embedded cover into the iPod's artwork
// database, so the iPod shows it. Failures are noted, not fatal.
func (a *App) addCover(d *ipod.Device, t *itdb.Track, file string, res *JobResult) {
	if t == nil || !d.CanWriteArtwork() {
		return
	}
	pic, err := media.ReadArtwork(file)
	if err != nil || pic == nil {
		return
	}
	a.withDB(func() { err = d.SetArtwork(t, pic.Data) })
	if err != nil && !strings.Contains(strings.Join(res.Notes, "\n"), "album art") {
		res.Notes = append(res.Notes, "Some album art couldn't be added to the iPod: "+err.Error())
	}
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
	MediaType   *int    `json:"mediaType"` // videos: 2 movie, 32 music video, 64 TV show
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
	if p.MediaType != nil && t.IsVideo() {
		switch mt := uint32(*p.MediaType); mt {
		case itdb.MediaMovie, itdb.MediaMusicVideo, itdb.MediaTVShow:
			t.SetMediaType(mt)
		}
	}
	if t.MediaType()&itdb.MediaTVShow != 0 {
		// the iPod's TV Shows menu groups episodes by show name
		t.SetStr(itdb.MhodTVShow, firstNonEmpty(t.Album(), t.Title()))
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
		var planned int64
		a.withDB(func() {
			for _, id := range ids {
				if t := d.DB.TrackByID(id); t != nil {
					planned += int64(t.Size())
				}
			}
		})
		xf := a.startTransfer("export", "from", planned)
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
			if err := copyFileCounted(src, dst, xf); err != nil {
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
