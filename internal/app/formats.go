package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"podmanager/internal/convert"
	"podmanager/internal/ipod"
	"podmanager/internal/itdb"
	"podmanager/internal/media"
)

// The Format Manager edits tags and covers of songs on the computer or the
// iPod, and converts them between formats with the user's own encoders.
// Everything it makes lives in Classic iPod Manager's main folder:
//
//	Encoders\<format>\   encoder programs (folders can be changed in Options)
//	Converted Music\Artist\Album\   a copy of every converted song
//	iPod Originals\Artist\Album\    iPod songs as they were before converting

// Folder names inside the main folder.
const (
	ConvertedVideosFolder = "Converted Videos"
	ConvertedFolder       = "Converted Music"
	OriginalsFolder       = "iPod Originals"
	tempFolder            = "Temp"
)

// SetMainFolder sets Classic iPod Manager's main folder and creates its sub-folders.
func (a *App) SetMainFolder(dir string) {
	a.mainDir = dir
	if dir == "" {
		return
	}
	a.encoderConfig().MakeFolders()
	os.MkdirAll(filepath.Join(dir, ConvertedFolder), 0o755)
	os.MkdirAll(filepath.Join(dir, OriginalsFolder), 0o755)
	os.MkdirAll(filepath.Join(dir, ConvertedVideosFolder), 0o755)
}

// MainFolder returns the main folder.
func (a *App) MainFolder() string { return a.mainDir }

type encoderPrefs struct {
	Folders map[string]string `json:"folders"`
	Choice  map[string]string `json:"choice"`
}

func (a *App) readEncoderPrefs() encoderPrefs {
	var ep encoderPrefs
	prefsMu.Lock()
	raw, ok := a.readPrefs()["encoders"]
	prefsMu.Unlock()
	if ok {
		json.Unmarshal(raw, &ep)
	}
	if ep.Folders == nil {
		ep.Folders = map[string]string{}
	}
	if ep.Choice == nil {
		ep.Choice = map[string]string{}
	}
	return ep
}

func (a *App) encoderConfig() convert.Config {
	ep := a.readEncoderPrefs()
	return convert.Config{Main: a.mainDir, Folders: ep.Folders, Choice: ep.Choice}
}

// EncoderInfo is what Options → Encoders shows.
type EncoderInfo struct {
	Main      string           `json:"main"`
	Writable  bool             `json:"writable"`
	Converted string           `json:"converted"`
	Originals string           `json:"originals"`
	Encoders  []convert.Status `json:"encoders"`
	Formats   []convert.Format `json:"formats"`
}

// Encoders reports the main folder and every format's encoder.
func (a *App) Encoders() (*EncoderInfo, error) {
	cfg := a.encoderConfig()
	info := &EncoderInfo{Main: a.mainDir, Encoders: cfg.Statuses(), Formats: convert.Formats}
	if a.mainDir != "" {
		info.Converted = filepath.Join(a.mainDir, ConvertedFolder)
		info.Originals = filepath.Join(a.mainDir, OriginalsFolder)
		if f, err := os.CreateTemp(a.mainDir, ".podmanager-write-test-*"); err == nil {
			f.Close()
			os.Remove(f.Name())
			info.Writable = true
		}
	}
	return info, nil
}

// SetEncoder changes a format's encoder folder (relative to the main folder
// or absolute; "" = the default) and the preferred program ("" = automatic).
func (a *App) SetEncoder(id, folder, choice string) (*EncoderInfo, error) {
	if _, ok := convert.FormatByID(id); !ok && id != "decoder" {
		return nil, fmt.Errorf("unknown format %q", id)
	}
	ep := a.readEncoderPrefs()
	folder = strings.TrimSpace(folder)
	if folder == "" {
		delete(ep.Folders, id)
	} else {
		ep.Folders[id] = folder
	}
	if choice == "" {
		delete(ep.Choice, id)
	} else {
		ep.Choice[id] = choice
	}
	b, _ := json.Marshal(ep)
	if err := a.SetPref("encoders", b); err != nil {
		return nil, err
	}
	if dir := a.encoderConfig().Dir(id); dir != "" {
		os.MkdirAll(dir, 0o755)
	}
	return a.Encoders()
}

// OpenFolder shows one of Classic iPod Manager's folders in Explorer: "main",
// "converted", "originals", "encoders", or a format's encoder folder.
func (a *App) OpenFolder(which string) error {
	if a.mainDir == "" {
		return errors.New("Classic iPod Manager has no main folder")
	}
	var dir string
	switch which {
	case "main":
		dir = a.mainDir
	case "converted":
		dir = filepath.Join(a.mainDir, ConvertedFolder)
	case "originals":
		dir = filepath.Join(a.mainDir, OriginalsFolder)
	case "encoders":
		dir = filepath.Join(a.mainDir, "Encoders")
	default:
		dir = a.encoderConfig().Dir(which)
	}
	if dir == "" {
		return fmt.Errorf("unknown folder %q", which)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return a.plat.OpenFolder(dir)
}

// ---------------------------------------------------------------- items

// FMItem is a song in the Format Manager's list.
type FMItem struct {
	Key     string            `json:"key"`    // "file:<path>" or "ipod:<id>"
	Source  string            `json:"source"` // "file" or "ipod"
	Path    string            `json:"path"`
	TrackID uint32            `json:"trackId,omitempty"`
	Name    string            `json:"name"` // file name
	Info    *media.SourceInfo `json:"info,omitempty"`
	Error   string            `json:"error,omitempty"`
}

const maxFMFiles = 5000

// FMInspect reads songs (and the songs inside folders) for the list.
func (a *App) FMInspect(paths []string) ([]FMItem, error) {
	var files []string
	seen := map[string]bool{}
	add := func(p string) {
		k := strings.ToLower(p)
		if !seen[k] && len(files) < maxFMFiles {
			seen[k] = true
			files = append(files, p)
		}
	}
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			add(p)
			continue
		}
		var found []string
		filepath.WalkDir(p, func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if strings.HasPrefix(e.Name(), ".") && path != p {
				if e.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !e.IsDir() && media.DecodableExt(path) {
				found = append(found, path)
			}
			return nil
		})
		sort.Strings(found)
		for _, f := range found {
			add(f)
		}
	}
	a.allowPlay(files...)
	items := make([]FMItem, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f string) {
			defer wg.Done()
			defer func() { <-sem }()
			items[i] = fileItem(f)
		}(i, f)
	}
	wg.Wait()
	return items, nil
}

func fileItem(path string) FMItem {
	it := FMItem{Key: "file:" + path, Source: "file", Path: path, Name: filepath.Base(path)}
	si, err := media.Inspect(path)
	if err != nil {
		var u media.ErrUnsupported
		if errors.As(err, &u) {
			it.Error = u.Reason
		} else {
			it.Error = "couldn't be read: " + err.Error()
		}
		return it
	}
	it.Info = si
	return it
}

// FMFromIPod adds songs from the iPod to the list.
func (a *App) FMFromIPod(ids []uint32) ([]FMItem, error) {
	a.mu.Lock()
	d := a.dev
	a.mu.Unlock()
	if d == nil {
		return nil, errors.New("no iPod is open")
	}
	var out []FMItem
	for _, id := range ids {
		var t *itdb.Track
		var path string
		var tags media.Tags
		a.withDB(func() {
			if t = d.DB.TrackByID(id); t != nil {
				path = d.AbsPath(t)
				tags = tagsOf(t)
			}
		})
		if t == nil {
			continue
		}
		out = append(out, a.ipodItem(d, t, path, tags))
	}
	return out, nil
}

func (a *App) ipodItem(d *ipod.Device, t *itdb.Track, path string, tags media.Tags) FMItem {
	it := fileItem(path)
	it.Key = fmt.Sprintf("ipod:%d", t.ID())
	it.Source = "ipod"
	it.TrackID = t.ID()
	if it.Info != nil {
		it.Info.Tags = tags // the iPod's database is what the iPod shows
		var has bool
		a.withDB(func() { has = t.HasArtwork() })
		if has {
			it.Info.HasArt = true
		}
	} else if it.Error != "" {
		it.Error = "the song's file on the iPod " + it.Error
	}
	return it
}

// RevealFile shows a file on the computer in Explorer.
func (a *App) RevealFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return a.plat.Reveal(path)
}

// FMPickFiles shows an Open dialog for songs of any format.
func (a *App) FMPickFiles() ([]string, error) {
	return a.plat.PickAny("Add Songs to the Format Manager", []FileFilter{
		{"Audio files", "*.mp3;*.m4a;*.m4b;*.mp4;*.aac;*.wav;*.aif;*.aiff;*.flac;*.ogg;*.oga;*.opus;*.wma;*.ape;*.wv;*.mpc;*.mka;*.caf"},
		{"All files", "*.*"},
	}, true)
}

// FMPickFolder shows a folder picker; songs inside it are added.
func (a *App) FMPickFolder() ([]string, error) {
	dir, err := a.plat.PickFolder("Choose a folder of songs for the Format Manager")
	if err != nil || dir == "" {
		return nil, err
	}
	return []string{dir}, nil
}

// FMImage is a picture chosen as a cover.
type FMImage struct {
	Path string   `json:"path"`
	Art  *Artwork `json:"art"`
}

// FMPickImage lets the user choose a cover picture.
func (a *App) FMPickImage() (*FMImage, error) {
	paths, err := a.plat.PickAny("Choose Album Artwork", []FileFilter{
		{"Pictures (JPEG, PNG, GIF)", "*.jpg;*.jpeg;*.png;*.gif"},
	}, false)
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	return a.FMImagePreview(paths[0])
}

// FMImagePreview checks a picture file and returns a preview of it.
func (a *App) FMImagePreview(path string) (*FMImage, error) {
	pic, err := loadPicture(path)
	if err != nil {
		return nil, err
	}
	art := encodeArt(pic.Data, pic.MIME)
	if art == nil {
		return nil, errors.New("the picture can't be shown")
	}
	art.Source = "file"
	return &FMImage{Path: path, Art: art}, nil
}

// FMArtwork returns the current cover of a list item.
func (a *App) FMArtwork(source, path string, trackID uint32) (*Artwork, error) {
	if source == "ipod" {
		return a.Artwork(trackID)
	}
	pic, err := media.ReadArtwork(path)
	if err != nil || pic == nil {
		return nil, nil
	}
	art := encodeArt(pic.Data, pic.MIME)
	if art != nil {
		art.Source = "file"
	}
	return art, nil
}

// loadPicture reads a cover picture, converting it to JPEG when it's very
// large or not JPEG/PNG.
func loadPicture(path string) (*media.Picture, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > 40<<20 {
		return nil, errors.New("the picture is too large (over 40 MB)")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("this picture format isn't supported – use a JPEG or PNG")
	}
	const maxSide = 1500
	if (format == "jpeg" || format == "png") && cfg.Width <= maxSide && cfg.Height <= maxSide && len(data) <= 4<<20 {
		mime := "image/jpeg"
		if format == "png" {
			mime = "image/png"
		}
		return &media.Picture{Data: data, MIME: mime}, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("the picture can't be read: %w", err)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, shrink(img, maxSide), &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return &media.Picture{Data: buf.Bytes(), MIME: "image/jpeg"}, nil
}

// ---------------------------------------------------------------- jobs

// FMEdit is one song to save or convert, with its pending changes.
type FMEdit struct {
	Key     string `json:"key"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	TrackID uint32 `json:"trackId"`
	Patch   Patch  `json:"patch"`
	Art     string `json:"art"`     // "" keep, "set" or "remove"
	ArtPath string `json:"artPath"` // picture file for "set"
}

// FMRequest asks the Format Manager to save changes or convert songs.
type FMRequest struct {
	Items     []FMEdit `json:"items"`
	Convert   bool     `json:"convert"`
	Format    string   `json:"format"`
	Quality   string   `json:"quality"`
	AddToIPod bool     `json:"addToIPod"`
	// Strip lists the details left out of converted files: "title",
	// "artist", "albumArtist", "album", "genre", "year", "track", "disc",
	// "composer", "grouping", "comment", "bpm", "compilation" and "artwork".
	// The iPod's own library keeps showing songs' details either way.
	Strip []string `json:"strip"`
}

// StripFields are the details the converter can leave out of new files.
var StripFields = []string{"title", "artist", "albumArtist", "album", "genre", "year", "track", "disc",
	"composer", "grouping", "comment", "bpm", "compilation", "artwork"}

// stripTags clears the listed details. It reports whether "artwork" was
// listed.
func stripTags(t *media.Tags, strip []string) (noArt bool) {
	for _, f := range strip {
		switch f {
		case "title":
			t.Title = ""
		case "artist":
			t.Artist = ""
		case "albumArtist":
			t.AlbumArtist = ""
		case "album":
			t.Album = ""
		case "genre":
			t.Genre = ""
		case "year":
			t.Year = 0
		case "track":
			t.Track, t.TrackTotal = 0, 0
		case "disc":
			t.Disc, t.DiscTotal = 0, 0
		case "composer":
			t.Composer = ""
		case "grouping":
			t.Grouping = ""
		case "comment":
			t.Comment = ""
		case "bpm":
			t.BPM = 0
		case "compilation":
			t.Compilation = false
		case "artwork":
			noArt = true
		}
	}
	return noArt
}

// FMOutcome reports what happened to one song.
type FMOutcome struct {
	Key    string  `json:"key"`
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Output string  `json:"output,omitempty"` // the converted copy
	Note   string  `json:"note,omitempty"`
	Item   *FMItem `json:"item,omitempty"` // refreshed details
}

func applyTagPatch(t *media.Tags, p *Patch) {
	s := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	n := func(dst *int, v *int) {
		if v != nil {
			*dst = clampU(*v)
		}
	}
	s(&t.Title, p.Title)
	s(&t.Artist, p.Artist)
	s(&t.Album, p.Album)
	s(&t.AlbumArtist, p.AlbumArtist)
	s(&t.Genre, p.Genre)
	s(&t.Composer, p.Composer)
	s(&t.Comment, p.Comment)
	s(&t.Grouping, p.Grouping)
	n(&t.Year, p.Year)
	n(&t.Track, p.TrackNumber)
	n(&t.TrackTotal, p.TrackCount)
	n(&t.Disc, p.DiscNumber)
	n(&t.DiscTotal, p.DiscCount)
	n(&t.BPM, p.BPM)
	if p.Compilation != nil {
		t.Compilation = *p.Compilation
	}
}

// pictures loads each chosen cover once.
type pictures struct {
	m map[string]*media.Picture
}

func (ps *pictures) edit(e FMEdit) (media.ArtEdit, error) {
	switch e.Art {
	case "remove":
		return media.ArtEdit{Remove: true}, nil
	case "set":
		if p, ok := ps.m[e.ArtPath]; ok {
			return media.ArtEdit{Set: p}, nil
		}
		p, err := loadPicture(e.ArtPath)
		if err != nil {
			return media.ArtEdit{}, fmt.Errorf("the cover picture: %w", err)
		}
		if ps.m == nil {
			ps.m = map[string]*media.Picture{}
		}
		ps.m[e.ArtPath] = p
		return media.ArtEdit{Set: p}, nil
	}
	return media.ArtEdit{}, nil
}

// FMRun saves edits or converts songs as a background job ("retag" or
// "convert").
func (a *App) FMRun(req FMRequest) error {
	if len(req.Items) == 0 {
		return errors.New("no songs selected")
	}
	needDev := req.Convert && req.AddToIPod
	for _, it := range req.Items {
		if it.Source == "ipod" {
			needDev = true
		}
	}
	if !req.Convert {
		return a.startTask("retag", false, needDev, func(ctx context.Context, d *ipod.Device, res *JobResult) error {
			return a.fmRetag(d, req, res)
		})
	}
	f, ok := convert.FormatByID(req.Format)
	if !ok {
		return fmt.Errorf("unknown format %q", req.Format)
	}
	if a.mainDir == "" {
		return errors.New("Classic iPod Manager has no main folder to save converted songs in")
	}
	if _, err := a.encoderConfig().Encoder(f.ID); err != nil {
		return err
	}
	return a.startTask("convert", false, needDev, func(ctx context.Context, d *ipod.Device, res *JobResult) error {
		return a.fmConvert(ctx, d, req, f, res)
	})
}

func itemLabel(e FMEdit) string {
	if e.Source == "ipod" {
		return fmt.Sprintf("iPod song %d", e.TrackID)
	}
	return e.Path
}

func (a *App) fmRetag(d *ipod.Device, req FMRequest, res *JobResult) error {
	var pics pictures
	touchedIPod := false
	noTags := 0
	for i, e := range req.Items {
		if a.cancel.Load() {
			break
		}
		out := FMOutcome{Key: e.Key}
		fail := func(msg string) {
			out.Error = msg
			res.Errors = append(res.Errors, JobError{itemLabel(e), msg})
			res.FM = append(res.FM, out)
		}
		art, err := pics.edit(e)
		if err != nil {
			fail(err.Error())
			continue
		}
		switch e.Source {
		case "file":
			a.progress("retag", i, len(req.Items), filepath.Base(e.Path))
			si, err := media.Inspect(e.Path)
			if err != nil {
				fail("couldn't be read: " + err.Error())
				continue
			}
			if !si.TagsInFile {
				noTags++
				fail(si.Label + " files can't store tags or artwork")
				continue
			}
			tags := si.Tags
			applyTagPatch(&tags, &e.Patch)
			if err := media.WriteTagsArt(e.Path, tags, art); err != nil {
				fail("couldn't save: " + err.Error())
				continue
			}
			it := fileItem(e.Path)
			out.Item = &it
		case "ipod":
			if d == nil {
				fail("no iPod is open")
				continue
			}
			var t *itdb.Track
			var tags media.Tags
			var path string
			a.withDB(func() {
				if t = d.DB.TrackByID(e.TrackID); t != nil {
					applyPatch(t, &e.Patch)
					tags = tagsOf(t)
					path = d.AbsPath(t)
				}
			})
			if t == nil {
				fail("the song is no longer on the iPod")
				continue
			}
			a.progress("retag", i, len(req.Items), tags.Title)
			touchedIPod = true
			if err := media.WriteTagsArt(path, tags, art); err != nil {
				if media.IsNoTagSupport(err) {
					noTags++
				} else {
					res.Errors = append(res.Errors, JobError{tags.Title, "couldn't update the file's tags: " + err.Error()})
				}
			} else if st, err := os.Stat(path); err == nil {
				a.withDB(func() { t.SetSize(uint32(st.Size())) })
			}
			if err := a.applyIPodArt(d, t, art); err != nil {
				res.Errors = append(res.Errors, JobError{tags.Title, err.Error()})
			}
			it := a.ipodItem(d, t, path, tags)
			out.Item = &it
		default:
			fail("unknown source")
			continue
		}
		out.OK = true
		res.OK++
		res.FM = append(res.FM, out)
	}
	if noTags > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d WAV/AIFF song(s) can't store tags or artwork in the file.", noTags))
	}
	if touchedIPod {
		a.progress("retag", len(req.Items), len(req.Items), "Updating iPod database…")
		var err error
		a.withDB(func() { err = d.Save() })
		return err
	}
	return nil
}

// applyIPodArt updates the iPod's own cover for a song.
func (a *App) applyIPodArt(d *ipod.Device, t *itdb.Track, art media.ArtEdit) error {
	if !d.CanWriteArtwork() {
		return nil
	}
	var err error
	switch {
	case art.Set != nil:
		a.withDB(func() { err = d.SetArtwork(t, art.Set.Data) })
	case art.Remove:
		a.withDB(func() { err = d.RemoveArtwork(t) })
	}
	if err != nil {
		return fmt.Errorf("couldn't update the cover on the iPod: %w", err)
	}
	return nil
}

// fmTask is one conversion.
type fmTask struct {
	n      int
	e      FMEdit
	job    convert.Job
	track  *itdb.Track
	art    media.ArtEdit
	backup string // where the iPod original goes
	err    error
	note   string
}

// pathAllocator hands out unique file names in the main folder while
// several conversions run at once.
type pathAllocator struct {
	mu   sync.Mutex
	used map[string]bool
}

func (pa *pathAllocator) libraryPath(base string, t media.Tags, ext string) string {
	artist := firstNonEmpty(t.AlbumArtist, t.Artist, "Unknown Artist")
	if t.Compilation {
		artist = "Compilations"
	}
	album := firstNonEmpty(t.Album, "Unknown Album")
	name := sanitize(firstNonEmpty(t.Title, "Untitled"))
	if t.Track > 0 {
		if t.DiscTotal > 1 && t.Disc > 0 {
			name = fmt.Sprintf("%d-%02d %s", t.Disc, t.Track, name)
		} else {
			name = fmt.Sprintf("%02d %s", t.Track, name)
		}
	}
	dir := filepath.Join(base, sanitize(artist), sanitize(album))
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if pa.used == nil {
		pa.used = map[string]bool{}
	}
	p := filepath.Join(dir, name+ext)
	for n := 2; ; n++ {
		_, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) && !pa.used[strings.ToLower(p)] {
			break
		}
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", name, n, ext))
	}
	pa.used[strings.ToLower(p)] = true
	return p
}

// videoPath is a unique file name for a converted video.
func (pa *pathAllocator) videoPath(base string, t media.Tags) string {
	name := sanitize(firstNonEmpty(t.Title, "Video"))
	if t.Artist != "" {
		name = sanitize(t.Artist) + " - " + name
	}
	pa.mu.Lock()
	defer pa.mu.Unlock()
	if pa.used == nil {
		pa.used = map[string]bool{}
	}
	p := filepath.Join(base, name+".m4v")
	for n := 2; ; n++ {
		_, err := os.Stat(p)
		if errors.Is(err, fs.ErrNotExist) && !pa.used[strings.ToLower(p)] {
			break
		}
		p = filepath.Join(base, fmt.Sprintf("%s (%d).m4v", name, n))
	}
	pa.used[strings.ToLower(p)] = true
	return p
}

func (a *App) fmConvert(ctx context.Context, d *ipod.Device, req FMRequest, f convert.Format, res *JobResult) error {
	cfg := a.encoderConfig()
	work := filepath.Join(a.mainDir, tempFolder)
	if err := os.MkdirAll(work, 0o755); err != nil {
		return fmt.Errorf("can't use the main folder: %w", err)
	}
	defer os.Remove(work)               // only if empty
	a.startTransfer("convert", "to", 0) // backups come from the iPod, new files go to it
	convertedBase := filepath.Join(a.mainDir, ConvertedFolder)
	originalsBase := filepath.Join(a.mainDir, OriginalsFolder)
	var alloc pathAllocator
	var pics pictures
	total := len(req.Items)

	// Prepare: read every source and work out its tags and destination.
	var tasks []*fmTask
	for i, e := range req.Items {
		t := &fmTask{n: i, e: e}
		tasks = append(tasks, t)
		art, err := pics.edit(e)
		if err != nil {
			t.err = err
			continue
		}
		t.art = art
		var src string
		var tags media.Tags
		switch e.Source {
		case "file":
			src = e.Path
		case "ipod":
			if d == nil {
				t.err = errors.New("no iPod is open")
				continue
			}
			a.withDB(func() {
				if t.track = d.DB.TrackByID(e.TrackID); t.track != nil {
					src = d.AbsPath(t.track)
					tags = tagsOf(t.track)
				}
			})
			if t.track == nil {
				t.err = errors.New("the song is no longer on the iPod")
				continue
			}
		default:
			t.err = errors.New("unknown source")
			continue
		}
		si, err := media.Inspect(src)
		if err != nil {
			t.err = fmt.Errorf("couldn't be read: %w", err)
			continue
		}
		if e.Source == "file" {
			tags = si.Tags
		}
		applyTagPatch(&tags, &e.Patch)
		forIPod := e.Source == "ipod" || (req.AddToIPod && f.IPod)
		// the copy is filed by artist and album even if those are left out
		out := alloc.libraryPath(convertedBase, tags, f.Ext)
		fileTags, fileArt := tags, art
		if stripTags(&fileTags, req.Strip) {
			fileArt = media.ArtEdit{Remove: true}
		}
		t.job = convert.Job{Src: src, Info: si, Format: f.ID, Quality: req.Quality, ForIPod: forIPod, Tags: fileTags,
			Art: fileArt, WorkDir: work, Out: out}
		if e.Source == "ipod" && f.IPod {
			t.backup = alloc.libraryPath(originalsBase, tagsOfTrackLocked(a, t.track), strings.ToLower(filepath.Ext(src)))
		}
		if err := cfg.Check(t.job); err != nil {
			t.err = err
		}
	}

	// Convert, several at a time.
	workers := runtime.NumCPU() / 2
	if workers > 4 {
		workers = 4
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan *fmTask)
	done := make(chan *fmTask)
	go func() {
		defer close(jobs)
		for _, t := range tasks {
			if t.err != nil {
				done <- t
				continue
			}
			select {
			case jobs <- t:
			case <-ctx.Done():
				t.err = context.Canceled
				done <- t
			}
		}
	}()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if ctx.Err() != nil {
					t.err = context.Canceled
					done <- t
					continue
				}
				// iPod songs are backed up before anything else happens
				if t.backup != "" {
					if err := os.MkdirAll(filepath.Dir(t.backup), 0o755); err != nil {
						t.err = fmt.Errorf("couldn't back up the original: %w", err)
					} else if err := copyFileCounted(t.job.Src, t.backup, a.currentTransfer()); err != nil {
						os.Remove(t.backup)
						t.err = fmt.Errorf("couldn't back up the original: %w", err)
					}
				}
				if t.err == nil {
					t.err = cfg.Run(ctx, t.job)
				}
				done <- t
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()

	// Finish each song as it comes back: iPod songs are replaced here, one
	// at a time, so the database is only touched from this goroutine.
	var toAdd []string
	var oldFiles []string
	finished, sinceSave := 0, 0
	saveIPod := func() error {
		if d == nil {
			return nil
		}
		var err error
		a.withDB(func() { err = d.Save() })
		if err != nil {
			return fmt.Errorf("could not update the iPod database: %w", err)
		}
		for _, p := range oldFiles {
			os.Remove(p)
		}
		oldFiles = nil
		sinceSave = 0
		return nil
	}
	a.progress("convert", 0, total, "Starting…")
	var fatal error
	for t := range done {
		finished++
		out := FMOutcome{Key: t.e.Key}
		title := t.job.Tags.Title
		if title == "" {
			title = filepath.Base(itemLabel(t.e))
		}
		a.progress("convert", finished, total, title)
		if t.err != nil {
			if errors.Is(t.err, context.Canceled) {
				continue
			}
			out.Error = t.err.Error()
			res.Errors = append(res.Errors, JobError{firstNonEmpty(title, itemLabel(t.e)), t.err.Error()})
			res.FM = append(res.FM, out)
			continue
		}
		out.OK, out.Output = true, t.job.Out
		res.OK++
		switch {
		case t.e.Source == "ipod" && f.IPod && fatal == nil:
			old, err := a.replaceOnIPod(d, t)
			if err != nil {
				out.OK = false
				out.Error = "converted, but couldn't replace the song on the iPod: " + err.Error()
				res.OK--
				res.Errors = append(res.Errors, JobError{title, out.Error})
				if isDiskFull(err) {
					fatal = errors.New("the iPod is full")
				}
				break
			}
			oldFiles = append(oldFiles, old)
			sinceSave++
			it := a.ipodItem(d, t.track, a.trackPath(d, t.track), tagsOfTrackLocked(a, t.track))
			out.Item = &it
			if sinceSave >= 10 {
				if err := saveIPod(); err != nil {
					fatal = err
				}
			}
		case t.e.Source == "ipod":
			// FLAC/Ogg can't go on the iPod: the copy is in Converted Music,
			// and only the song's details change on the iPod.
			a.withDB(func() { applyPatch(t.track, &t.e.Patch) })
			if err := a.applyIPodArt(d, t.track, t.art); err != nil {
				res.Errors = append(res.Errors, JobError{title, err.Error()})
			}
			sinceSave++
			out.Note = f.Label + " can't play on an iPod, so the song on the iPod was kept"
		case req.AddToIPod && f.IPod:
			toAdd = append(toAdd, t.job.Out)
		}
		res.FM = append(res.FM, out)
	}
	if fatal != nil {
		saveIPod()
		return fatal
	}
	if sinceSave > 0 || len(oldFiles) > 0 {
		a.progress("convert", total, total, "Updating iPod database…")
		if err := saveIPod(); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 && d != nil && !a.cancel.Load() {
		sub := &JobResult{Errors: []JobError{}, Notes: []string{}}
		if err := a.addFiles(ctx, d, "convert", toAdd, 0, sub); err != nil {
			return err
		}
		if sub.OK > 0 {
			res.Notes = append(res.Notes, fmt.Sprintf("Added %d converted %s to the iPod.", sub.OK, map[bool]string{true: "song", false: "songs"}[sub.OK == 1]))
		}
		res.Notes = append(res.Notes, sub.Notes...)
		res.Errors = append(res.Errors, sub.Errors...)
	}
	if res.OK > 0 {
		res.Notes = append(res.Notes, "Converted copies are in "+convertedBase)
	}
	if !f.IPod && len(toAdd) == 0 && req.AddToIPod {
		res.Notes = append(res.Notes, f.Label+" can't play on an iPod, so nothing was added to it.")
	}
	return nil
}

func tagsOfTrackLocked(a *App, t *itdb.Track) media.Tags {
	var tg media.Tags
	a.withDB(func() { tg = tagsOf(t) })
	return tg
}

func (a *App) trackPath(d *ipod.Device, t *itdb.Track) string {
	var p string
	a.withDB(func() { p = d.AbsPath(t) })
	return p
}

// replaceOnIPod copies a converted song onto the iPod and points its track
// at the new file. It returns the old file, to delete once the database is
// saved.
func (a *App) replaceOnIPod(d *ipod.Device, t *fmTask) (string, error) {
	info, err := media.Probe(t.job.Out)
	if err != nil {
		return "", err
	}
	abs, loc, err := d.AllocateFile(info.Ext)
	if err != nil {
		return "", err
	}
	if err := copyFileCounted(t.job.Out, abs, a.currentTransfer()); err != nil {
		os.Remove(abs)
		return "", err
	}
	old := a.trackPath(d, t.track)
	a.withDB(func() {
		applyPatch(t.track, &t.e.Patch)
		t.track.SetFile(itdb.FileInfo{Location: loc, FileTypeCode: info.FileTypeCode, Kind: info.Kind, Size: uint32(info.Size),
			LengthMs: info.LengthMs, Bitrate: info.Bitrate, SampleRate: info.SampleRate, VBR: info.VBR,
			IsAAC: info.Format != "mp3", SampleCount: info.SampleCount})
	})
	// The cover: a new one replaces the iPod's; otherwise keep what the
	// iPod has, or add the converted file's cover if it had none.
	switch {
	case t.art.Set != nil || t.art.Remove:
		if err := a.applyIPodArt(d, t.track, t.art); err != nil {
			return old, nil // the song itself was replaced fine
		}
	default:
		var has bool
		a.withDB(func() { has = t.track.HasArtwork() })
		if !has {
			a.addCover(d, t.track, abs, &JobResult{})
		}
	}
	return old, nil
}
