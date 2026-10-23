package app

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"podmanager/internal/ipod"
	"podmanager/internal/itdb"
	"podmanager/internal/media"
)

func testVideo(t *testing.T, path, size string, args ...string) {
	t.Helper()
	a := []string{"-nostdin", "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=30:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4:sample_rate=44100", "-shortest"}
	a = append(a, args...)
	if b, err := exec.Command("ffmpeg", append(a, "-y", path)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
}

func TestVideos(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	base := t.TempDir()
	root := filepath.Join(base, "ipod")
	vids := filepath.Join(base, "videos")
	main := filepath.Join(base, "Main")
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	os.MkdirAll(vids, 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("ModelNumStr: xA448\n"), 0o644)
	baseline := []string{"-c:v", "libx264", "-profile:v", "baseline", "-level", "3.0", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-metadata", "title=Holiday Clip", "-metadata", "artist=Harry"}
	testVideo(t, filepath.Join(vids, "holiday.m4v"), "320x240", baseline...)
	testVideo(t, filepath.Join(vids, "concert.mp4"), "1280x720", "-c:v", "libx264", "-profile:v", "high", "-pix_fmt", "yuv420p", "-c:a", "aac",
		"-metadata", "title=Concert", "-metadata", "artist=Band")
	testVideo(t, filepath.Join(vids, "old clip.avi"), "320x240", "-c:v", "mpeg4", "-c:a", "libmp3lame")
	song := filepath.Join(vids, "song.mp4") // audio-only .mp4: a song, not a video
	if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "sine=duration=3", "-c:a", "aac", "-metadata", "title=Just Audio", "-y", song).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}

	plat := &fakePlat{root: root, pick: vids}
	ev := make(chan Event, 2000)
	a := New(plat, t.TempDir(), func(e Event) { ev <- e })
	a.SetPrefsFile(filepath.Join(base, "settings.json"))
	a.SetMainFolder(main)
	if _, err := a.Initialize(root, "Video iPod"); err != nil {
		t.Fatal(err)
	}

	// without FFmpeg: the playable video is accepted, the others explain why not
	c := a.CheckPaths([]string{filepath.Join(vids, "holiday.m4v"), filepath.Join(vids, "concert.mp4"), filepath.Join(vids, "old clip.avi")})
	if len(c.Accepted) != 1 || c.Videos != 1 || c.Convert != 0 || len(c.Rejected) != 2 || !strings.Contains(c.Rejected[0].Message, "ffmpeg.exe") {
		t.Fatalf("check without ffmpeg: %+v", c)
	}
	linkTool(t, filepath.Join(main, "Encoders", "Decoder"), "ffmpeg")
	c = a.CheckPaths([]string{vids})
	if c.Videos != 3 || c.Convert != 2 || len(c.Rejected) != 0 || c.Songs != 4 {
		t.Fatalf("folder check with ffmpeg: %+v", c)
	}

	// "Add Folder" in the Music view only takes the song
	a.AddMedia([]string{vids}, "music", 0)
	if r := waitDone(t, ev); r.OK != 1 || r.Videos != 0 {
		t.Fatalf("music folder: %+v", r)
	}
	// and in the Videos view only the videos, converting two of them
	a.AddMedia([]string{vids}, "video", 0)
	r := waitDone(t, ev)
	if r.Error != "" || r.OK != 3 || r.Videos != 3 || r.Converted != 2 || len(r.Errors) != 0 {
		t.Fatalf("video folder: %+v", r)
	}
	// again: all duplicates
	a.AddMedia([]string{vids}, "video", 0)
	if r := waitDone(t, ev); r.OK != 0 || r.Skipped != 3 {
		t.Fatalf("duplicates: %+v", r)
	}
	lib, _ := a.Library()
	if len(lib.Tracks) != 4 || lib.Video == 0 || lib.Music == 0 {
		t.Fatalf("library: %d tracks, video %d, music %d", len(lib.Tracks), lib.Video, lib.Music)
	}
	var concert TrackView
	for _, tr := range lib.Tracks {
		if tr.Title == "Just Audio" {
			if tr.Video {
				t.Fatal("song marked as video")
			}
			continue
		}
		if !tr.Video || tr.MediaType != itdb.MediaMovie || tr.Kind != "MPEG-4 video file" || !strings.HasSuffix(tr.Path, ".m4v") || tr.LengthMs < 3800 {
			t.Fatalf("video track: %+v", tr)
		}
		if tr.Title == "Concert" {
			concert = tr
		}
		vi, err := media.ProbeVideo(filepath.Join(root, filepath.FromSlash(tr.Path)))
		if err != nil || !vi.Playable {
			t.Fatalf("%s on the iPod isn't playable: %+v %v", tr.Title, vi, err)
		}
	}
	if concert.ID == 0 {
		t.Fatal("converted video keeps its title")
	}
	// converted copies kept in the main folder
	if m, _ := filepath.Glob(filepath.Join(main, "Converted Videos", "*.m4v")); len(m) != 2 {
		t.Fatalf("converted copies: %v", m)
	}
	// every video got a thumbnail on the iPod
	d, err := ipod.Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range d.DB.Tracks {
		if tr.IsVideo() && !tr.HasArtwork() {
			t.Fatalf("%s has no thumbnail", tr.Title())
		}
	}

	// make the concert a TV show; it gets a show name
	tv := int(itdb.MediaTVShow)
	album := "Live Shows"
	if err := a.Update([]uint32{concert.ID}, Patch{MediaType: &tv, Album: &album}, false); err != nil {
		t.Fatal(err)
	}
	waitDone(t, ev)
	d, _ = ipod.Open(root, "")
	ct := d.DB.TrackByID(concert.ID)
	if ct.MediaType() != itdb.MediaTVShow || ct.Str(itdb.MhodTVShow) != "Live Shows" || !ct.IsVideo() {
		t.Fatalf("tv show: mt %d show %q", ct.MediaType(), ct.Str(itdb.MhodTVShow))
	}

	// libgpod's view
	if tool := os.Getenv("LIBGPOD_VIDCHECK"); tool != "" {
		out, err := exec.Command(tool, root).CombinedOutput()
		if err != nil || strings.Contains(string(out), "ERROR") {
			t.Fatalf("libgpod: %v\n%s", err, out)
		}
		t.Logf("libgpod:\n%s", out)
		if strings.Count(string(out), "movie=1|type=MPEG-4 video file") != 3 || !strings.Contains(string(out), "mt=64|movie=1") ||
			!strings.Contains(string(out), "tvshow=Live Shows") || !strings.Contains(string(out), "master=4") {
			t.Fatal("libgpod doesn't see the videos as expected")
		}
	}

	// adding into a section files the video there; the file's own kind
	// (iTunes' stik) is used when added to Videos in general
	mv := filepath.Join(vids, "promo.m4v")
	testVideo(t, mv, "320x240", append(append([]string{}, baseline...), "-metadata", "title=Promo")...)
	a.AddMedia([]string{mv}, "video", int(itdb.MediaMusicVideo))
	if r := waitDone(t, ev); r.OK != 1 {
		t.Fatalf("add to music videos: %+v", r)
	}
	ep := filepath.Join(vids, "episode.m4v")
	testVideo(t, ep, "320x240", append(append([]string{}, baseline...), "-metadata", "title=Pilot", "-metadata", "album=Space Show", "-movflags", "+faststart")...)
	if b, err := exec.Command("ffmpeg", "-nostdin", "-v", "error", "-i", ep, "-c", "copy", "-metadata", "media_type=10", "-y", ep+".tmp.m4v").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, b)
	}
	os.Rename(ep+".tmp.m4v", ep)
	if media.MP4MediaKind(ep) != 10 {
		t.Fatalf("stik: %d", media.MP4MediaKind(ep))
	}
	a.AddMedia([]string{ep}, "video", 0)
	if r := waitDone(t, ev); r.OK != 1 {
		t.Fatalf("add episode: %+v", r)
	}
	d, _ = ipod.Open(root, "")
	for _, tr := range d.DB.Tracks {
		switch tr.Title() {
		case "Promo":
			if tr.MediaType() != itdb.MediaMusicVideo {
				t.Fatalf("promo filed as %d", tr.MediaType())
			}
		case "Pilot":
			if tr.MediaType() != itdb.MediaTVShow || tr.Str(itdb.MhodTVShow) != "Space Show" {
				t.Fatalf("pilot filed as %d / %q", tr.MediaType(), tr.Str(itdb.MhodTVShow))
			}
		}
	}

	// remove a video: file and thumbnail go
	p := filepath.Join(root, filepath.FromSlash(concert.Path))
	if err := a.Remove([]uint32{concert.ID}); err != nil {
		t.Fatal(err)
	}
	waitDone(t, ev)
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("video file still on the iPod")
	}
	lib, _ = a.Library()
	if len(lib.Tracks) != 5 {
		t.Fatalf("after remove: %d", len(lib.Tracks))
	}
}
