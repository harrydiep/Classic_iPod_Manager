package app

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"podmanager/internal/convert"
	"podmanager/internal/media"
)

// Playback: the interface plays songs with an <audio>/<video> element. The
// files are served by a small HTTP handler on the loopback address (the
// Windows build listens on 127.0.0.1 with a random port; the development
// server mounts it on its own port). Requests need a random token.
//
//	/media/track/<id>?t=<token>[&decode=1]   a song or video on the iPod
//	/media/file?path=<path>&t=<token>[&decode=1]   a file listed in the Format Manager
//
// decode=1 serves the song as WAV, decoded with the user's encoders, for
// formats the player can't open itself (AIFF, Apple Lossless…).

type player struct {
	once    sync.Once
	token   string
	base    string
	mu      sync.Mutex
	allowed map[string]bool // computer files that may be played
	decMu   sync.Mutex      // one decode at a time
}

func (a *App) mediaToken() string {
	a.play.once.Do(func() {
		var b [16]byte
		rand.Read(b[:])
		a.play.token = hex.EncodeToString(b[:])
	})
	return a.play.token
}

// SetMediaBase tells the app where MediaHandler is served ("http://127.0.0.1:1234/").
func (a *App) SetMediaBase(base string) { a.play.base = base }

// MediaInfo is what the interface needs to build playback URLs.
type MediaInfo struct {
	Base  string `json:"base"`
	Token string `json:"token"`
}

// MediaInfo returns the playback server's address and token.
func (a *App) MediaInfo() (*MediaInfo, error) {
	return &MediaInfo{Base: a.play.base, Token: a.mediaToken()}, nil
}

func (a *App) allowPlay(paths ...string) {
	a.play.mu.Lock()
	defer a.play.mu.Unlock()
	if a.play.allowed == nil {
		a.play.allowed = map[string]bool{}
	}
	for _, p := range paths {
		a.play.allowed[filepath.Clean(p)] = true
	}
}

var mediaTypes = map[string]string{
	".mp3": "audio/mpeg", ".m4a": "audio/mp4", ".m4b": "audio/mp4", ".m4p": "audio/mp4", ".mp4": "video/mp4",
	".m4v": "video/mp4", ".mov": "video/quicktime", ".wav": "audio/wav", ".aif": "audio/aiff", ".aiff": "audio/aiff",
	".aac": "audio/aac", ".flac": "audio/flac", ".ogg": "audio/ogg", ".oga": "audio/ogg", ".opus": "audio/ogg",
}

// MediaHandler serves files for playback.
func (a *App) MediaHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Query().Get("t") != a.mediaToken() {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var path string
		switch {
		case strings.HasPrefix(r.URL.Path, "/media/track/"):
			id, err := strconv.ParseUint(strings.TrimPrefix(r.URL.Path, "/media/track/"), 10, 32)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			a.mu.Lock()
			if d := a.dev; d != nil {
				if t := d.DB.TrackByID(uint32(id)); t != nil {
					path = d.AbsPath(t)
				}
			}
			a.mu.Unlock()
		case r.URL.Path == "/media/file":
			p := filepath.Clean(r.URL.Query().Get("path"))
			a.play.mu.Lock()
			ok := a.play.allowed[p]
			a.play.mu.Unlock()
			if ok {
				path = p
			}
		}
		if path == "" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("decode") == "1" {
			dec, err := a.decodeForPlayback(r.Context(), path)
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
				return
			}
			path = dec
		}
		f, err := os.Open(path)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if ct := mediaTypes[strings.ToLower(filepath.Ext(path))]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		http.ServeContent(w, r, "", st.ModTime(), f) // handles Range requests (seeking)
	})
}

func (a *App) playCacheDir() string {
	if a.mainDir != "" {
		return filepath.Join(a.mainDir, tempFolder, "Playback")
	}
	return filepath.Join(os.TempDir(), "ClassiciPodManager-playback")
}

// decodeForPlayback converts a song to a WAV file in the playback cache
// (kept for the five most recent songs) and returns its path.
func (a *App) decodeForPlayback(ctx context.Context, src string) (string, error) {
	st, err := os.Stat(src)
	if err != nil {
		return "", err
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s|%d|%d", src, st.Size(), st.ModTime().UnixNano())))
	dir := a.playCacheDir()
	out := filepath.Join(dir, hex.EncodeToString(sum[:8])+".wav")
	a.play.decMu.Lock()
	defer a.play.decMu.Unlock()
	if _, err := os.Stat(out); err == nil {
		now := time.Now()
		os.Chtimes(out, now, now)
		return out, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	info, err := media.Inspect(src)
	if err != nil {
		return "", err
	}
	cfg := a.encoderConfig()
	job := convert.Job{Src: src, Info: info, Format: "wav", ForIPod: true, Tags: info.Tags, Out: out, WorkDir: dir}
	if err := cfg.Run(ctx, job); err != nil {
		var nd convert.ErrNoDecoder
		if errors.As(err, &nd) {
			return "", fmt.Errorf("%s can't be played without a decoder – put ffmpeg.exe in %s", info.Label, nd.Dir)
		}
		return "", err
	}
	// keep the cache small
	old, _ := filepath.Glob(filepath.Join(dir, "*.wav"))
	if len(old) > 5 {
		sort.Slice(old, func(i, j int) bool {
			si, _ := os.Stat(old[i])
			sj, _ := os.Stat(old[j])
			return si != nil && sj != nil && si.ModTime().Before(sj.ModTime())
		})
		for _, p := range old[:len(old)-5] {
			if p != out {
				os.Remove(p)
			}
		}
	}
	return out, nil
}

// ClearPlaybackCache removes decoded songs (called at start-up).
func (a *App) ClearPlaybackCache() { os.RemoveAll(a.playCacheDir()) }
