package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMediaHandler(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	aiff := filepath.Join(dir, "tone.aiff")
	mp3 := filepath.Join(dir, "tone.mp3")
	for _, f := range [][]string{{aiff, "pcm_s16be"}, {mp3, "libmp3lame"}} {
		if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=duration=2", "-c:a", f[1], "-y", f[0]).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, b)
		}
	}
	a := New(&fakePlat{}, "", func(Event) {})
	a.SetMainFolder(filepath.Join(dir, "Main"))
	srv := httptest.NewServer(a.MediaHandler())
	defer srv.Close()
	info, _ := a.MediaInfo()
	get := func(q url.Values, hdr map[string]string) *http.Response {
		req, _ := http.NewRequest("GET", srv.URL+"/media/file?"+q.Encode(), nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// not listed in the Format Manager: refused
	if r := get(url.Values{"path": {mp3}, "t": {info.Token}}, nil); r.StatusCode != 404 {
		t.Fatalf("unlisted file: %d", r.StatusCode)
	}
	a.FMInspect([]string{dir})
	if r := get(url.Values{"path": {mp3}, "t": {"wrong"}}, nil); r.StatusCode != 403 {
		t.Fatalf("bad token: %d", r.StatusCode)
	}
	r := get(url.Values{"path": {mp3}, "t": {info.Token}}, map[string]string{"Range": "bytes=100-199"})
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 206 || len(b) != 100 || r.Header.Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("range: %d %d %s", r.StatusCode, len(b), r.Header.Get("Content-Type"))
	}
	r = get(url.Values{"path": {aiff}, "t": {info.Token}, "decode": {"1"}}, nil)
	b, _ = io.ReadAll(r.Body)
	if r.StatusCode != 200 || string(b[:4]) != "RIFF" || r.Header.Get("Content-Type") != "audio/wav" {
		t.Fatalf("decode: %d %q %s", r.StatusCode, b[:4], r.Header.Get("Content-Type"))
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "Main", "Temp", "Playback", "*.wav")); len(m) != 1 {
		t.Fatalf("cache: %v", m)
	}
	a.ClearPlaybackCache()
	if _, err := os.Stat(filepath.Join(dir, "Main", "Temp", "Playback")); !os.IsNotExist(err) {
		t.Fatal("cache not cleared")
	}
}
