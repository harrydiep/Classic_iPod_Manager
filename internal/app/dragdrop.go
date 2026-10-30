package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"podmanager/internal/media"
)

// DropCheck is the verdict on files dragged onto the window.
type DropCheck struct {
	Accepted []string   `json:"accepted"` // files and folders that will be copied
	Songs    int        `json:"songs"`    // songs found (including inside folders)
	Videos   int        `json:"videos"`   // videos found
	Convert  int        `json:"convert"`  // videos that will be converted with FFmpeg first
	Rejected []JobError `json:"rejected"` // files the iPod can't play
	Reason   string     `json:"reason,omitempty"`
	Paths    []string   `json:"paths"` // everything that was dropped
}

// CheckPaths sorts dropped files into ones the iPod can play and ones it
// can't. Single files are inspected properly; folders are scanned by file
// extension (the copy itself still checks every song).
func (a *App) CheckPaths(paths []string) *DropCheck {
	res := &DropCheck{Accepted: []string{}, Rejected: []JobError{}}
	a.mu.Lock()
	switch {
	case a.dev == nil:
		res.Reason = "Connect and open an iPod before adding songs."
	case a.dev.ReadOnly != "":
		res.Reason = a.dev.ReadOnly
	case a.busy:
		res.Reason = "Please wait for the current task to finish, then drop the songs again."
	}
	a.mu.Unlock()
	if res.Reason != "" {
		return res
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		st, err := os.Stat(p)
		if err != nil {
			res.Rejected = append(res.Rejected, JobError{p, "couldn't be read"})
			continue
		}
		if st.IsDir() {
			ok, bad := 0, 0
			ff := a.encoderConfig().FFmpeg() != nil
			filepath.WalkDir(p, func(path string, e fs.DirEntry, err error) error {
				if err != nil || strings.HasPrefix(e.Name(), ".") {
					if e != nil && e.IsDir() && path != p {
						return filepath.SkipDir
					}
					return nil
				}
				if e.IsDir() {
					return nil
				}
				if media.VideoExt(path) && (!media.KnownAudioExt(path) || media.IsVideo(path)) {
					if v := a.checkVideo(path, ff); v.Reason != "" {
						bad++
						res.Rejected = append(res.Rejected, JobError{path, v.Reason})
					} else {
						ok++
						res.Videos++
						if v.convert {
							res.Convert++
						}
					}
					return nil
				}
				if !media.KnownAudioExt(path) {
					return nil
				}
				if media.SupportedExt(path) {
					ok++
				} else {
					bad++
					res.Rejected = append(res.Rejected, JobError{path, unsupportedReason(path)})
				}
				return nil
			})
			if ok > 0 {
				res.Accepted = append(res.Accepted, p)
				res.Songs += ok
			} else if bad == 0 {
				res.Rejected = append(res.Rejected, JobError{p, "folder contains no songs"})
			}
			continue
		}
		if media.IsVideo(p) {
			v := a.checkVideo(p, a.encoderConfig().FFmpeg() != nil)
			if v.Reason != "" {
				res.Rejected = append(res.Rejected, JobError{p, v.Reason})
				continue
			}
			res.Accepted = append(res.Accepted, p)
			res.Songs++
			res.Videos++
			if v.convert {
				res.Convert++
			}
			continue
		}
		if _, err := media.Probe(p); err != nil {
			var u media.ErrUnsupported
			msg := err.Error()
			if !errors.As(err, &u) {
				msg = "couldn't be read: " + msg
			}
			if !media.KnownAudioExt(p) {
				msg = "isn't a song file"
			}
			res.Rejected = append(res.Rejected, JobError{p, msg})
			continue
		}
		res.Accepted = append(res.Accepted, p)
		res.Songs++
	}
	return res
}

func unsupportedReason(p string) string {
	ext := strings.ToUpper(strings.TrimPrefix(filepath.Ext(p), "."))
	return ext + " isn't playable on an iPod classic"
}

// HandleDrop is called with the paths of files dropped on the window. The
// verdict is sent to the interface, which confirms and starts the copy.
func (a *App) HandleDrop(paths []string) {
	c := a.CheckPaths(paths)
	c.Paths = paths
	a.emit(Event{Type: "drop", Drop: c})
}

// DragFile is one song offered to Explorer when dragging out of the list.
type DragFile struct {
	Path string // file on the iPod
	Name string // name it gets on the computer
	Size int64
}

// DragOut starts a native drag of the given songs, so they can be dropped
// into an Explorer window or onto the desktop. It returns when the drag ends.
func (a *App) DragOut(ids []uint32) error {
	a.mu.Lock()
	d := a.dev
	var files []DragFile
	used := map[string]int{}
	if d != nil {
		for _, id := range ids {
			t := d.DB.TrackByID(id)
			if t == nil {
				continue
			}
			p := d.AbsPath(t)
			st, err := os.Stat(p)
			if err != nil {
				continue
			}
			base := sanitize(t.Title())
			if ar := firstNonEmpty(t.Artist(), t.AlbumArtist()); ar != "" {
				base = sanitize(ar) + " - " + base
			}
			ext := strings.ToLower(filepath.Ext(p))
			name := base + ext
			key := strings.ToLower(name)
			if n := used[key]; n > 0 {
				name = fmt.Sprintf("%s (%d)%s", base, n+1, ext)
			}
			used[key]++
			files = append(files, DragFile{Path: p, Name: name, Size: st.Size()})
		}
	}
	a.mu.Unlock()
	if d == nil {
		return errors.New("no iPod is open")
	}
	if len(files) == 0 {
		return errors.New("the selected songs couldn't be found on the iPod")
	}
	return a.plat.DragOut(files)
}

type videoCheck struct {
	convert bool
	Reason  string
}

// checkVideo decides whether a video can go on the iPod: as it is, after
// converting it with FFmpeg, or not at all (Reason says why).
func (a *App) checkVideo(path string, haveFFmpeg bool) videoCheck {
	if !a.playsVideo() {
		return videoCheck{Reason: "this iPod doesn't play video"}
	}
	vi, err := media.ProbeVideo(path)
	if err != nil {
		return videoCheck{Reason: "couldn't be read: " + err.Error()}
	}
	if vi.Playable {
		return videoCheck{}
	}
	if haveFFmpeg {
		return videoCheck{convert: true}
	}
	return videoCheck{Reason: vi.Reason + " – put ffmpeg.exe in " + a.encoderConfig().Dir("decoder") + " to convert it"}
}

// playsVideo reports whether the open iPod plays video (true when none is
// open, so checks don't refuse too early).
func (a *App) playsVideo() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.dev == nil || a.dev.Model.Video
}
