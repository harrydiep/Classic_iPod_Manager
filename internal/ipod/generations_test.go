package ipod

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"podmanager/internal/itdb"
)

func fakeIPod(t *testing.T, sysinfo string) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "iPod_Control", "Device"), 0o755)
	if sysinfo != "" {
		os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte(sysinfo), 0o644)
	}
	return root
}

func TestReadModel(t *testing.T) {
	for _, c := range []struct {
		sysinfo, name, gen      string
		video, art, shuffle, ok bool
		sum                     Checksum
	}{
		{"ModelNumStr: xA147\n", "iPod 60GB", "5th generation", true, true, false, true, ChecksumNone},
		{"ModelNumStr: M9282LL/A\n", "iPod 20GB", "4th generation", false, false, false, true, ChecksumNone},
		{"ModelNumStr: x9800\n", "iPod mini 4GB", "2nd generation", false, false, false, true, ChecksumNone},
		{"ModelNumStr: xA079\n", "iPod photo 20GB", "4th generation (colour display)", false, true, false, true, ChecksumNone},
		{"ModelNumStr: xA350\n", "iPod nano 1GB", "1st generation", false, true, false, true, ChecksumNone},
		{"ModelNumStr: xB029\n", "iPod classic 80GB", "6th generation", true, true, false, true, ChecksumHash58},
		{"ModelNumStr: xA978\n", "iPod nano 4GB", "3rd generation", true, true, false, true, ChecksumHash58},
		{"ModelNumStr: xC060\n", "iPod nano 16GB", "5th generation", true, false, false, false, ChecksumHash72},
		{"ModelNumStr: x9724\n", "iPod shuffle 512MB", "1st generation", false, false, true, true, ChecksumNone},
		{"ModelNumStr: xC584\n", "iPod shuffle 2GB", "4th generation", false, false, true, true, ChecksumNone},
		{"pszSerialNumber: 8K7140XXY5N\n", "iPod classic 80GB", "6th generation", true, true, false, true, ChecksumHash58},
		{"", "iPod", "", true, false, false, true, ChecksumNone},
	} {
		root := fakeIPod(t, c.sysinfo)
		m, _ := ReadModel(root)
		if m.Name != c.name || m.Generation != c.gen || m.Video != c.video || m.Artwork != c.art || m.Shuffle != c.shuffle ||
			m.Supported != c.ok || m.checksum != c.sum {
			t.Errorf("%q: %+v", c.sysinfo, m)
		}
	}
	// SysInfoExtended's database version wins
	root := fakeIPod(t, "ModelNumStr: xA147\n")
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfoExtended"),
		[]byte("<plist><dict><key>DBVersion</key><integer>3</integer><key>FireWireGUID</key><string>000A2700138A4A0D</string></dict></plist>"), 0o644)
	if m, _ := ReadModel(root); m.checksum != ChecksumHash58 {
		t.Fatalf("SysInfoExtended DBVersion ignored: %v", m.checksum)
	}
}

func addFakeTrack(t *testing.T, d *Device, title string) *itdb.Track {
	t.Helper()
	abs, loc, err := d.AllocateFile(".mp3")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(abs, []byte("not really an mp3"), 0o644)
	tr := d.DB.NewTrack(itdb.NewTrackInfo{Title: title, Artist: "Band", Album: "Record", Location: loc,
		FileTypeCode: "MP3 ", Kind: "MPEG audio file", Size: 17, LengthMs: 1000})
	return tr
}

// The classic signs its database; without its FireWire ID it's read-only.
func TestClassicSigned(t *testing.T) {
	root := fakeIPod(t, "ModelNumStr: xB029\n")
	if _, err := Initialize(root, "Classic", t.TempDir()); err == nil || !strings.Contains(err.Error(), "FireWire") {
		t.Fatalf("no FireWire ID: %v", err)
	}
	// found through the drive's serial number
	SerialLookup = func(string) string { return "000A2700138A4A0D" }
	defer func() { SerialLookup = nil }()
	d, err := Initialize(root, "Classic", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := os.ReadDir(filepath.Join(root, "iPod_Control", "Music")); len(n) != 50 {
		t.Fatalf("music folders: %d", len(n))
	}
	tr := addFakeTrack(t, d, "First")
	addFakeTrack(t, d, "Second")
	if err := d.SetArtwork(tr, solid(600, 600, color.RGBA{200, 30, 30, 255}, false)); err != nil {
		t.Fatal(err)
	}
	if err := d.Save(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesDB"))
	check := append([]byte(nil), b...)
	signHash58(check, "000A2700138A4A0D")
	if binary.LittleEndian.Uint16(b[0x30:]) != 1 || !bytes.Equal(check, b) || bytes.Equal(b[0x58:0x6C], make([]byte, 20)) {
		t.Fatal("database isn't signed correctly")
	}
	// all four classic cover sizes
	art, _ := os.ReadDir(filepath.Join(root, "iPod_Control", "Artwork"))
	var names []string
	for _, e := range art {
		names = append(names, e.Name())
	}
	for _, f := range []string{"F1060_1.ithmb", "F1055_1.ithmb", "F1068_1.ithmb", "F1061_1.ithmb"} {
		if !strings.Contains(strings.Join(names, " "), f) {
			t.Fatalf("artwork files: %v", names)
		}
	}
	d2, err := Open(root, "")
	if err != nil || d2.ReadOnly != "" || len(d2.DB.Tracks) != 2 {
		t.Fatalf("reopen: %v %q", err, d2.ReadOnly)
	}
	if img, _ := d2.DBArtwork(tr.DBID()); img == nil || img.Bounds().Dx() != 320 {
		t.Fatal("cover not readable")
	}
	if tool := os.Getenv("ITDB_LIBGPOD_DUMP"); tool != "" {
		out, err := exec.Command(tool, "dump", root).CombinedOutput()
		if err != nil || !strings.Contains(string(out), "title=[Second]") {
			t.Fatalf("libgpod: %v\n%s", err, out)
		}
	}
}

// Unknown models: the database's own signature decides.
func TestUnknownModelSigned(t *testing.T) {
	root := fakeIPod(t, "")
	db := itdb.New("Mystery")
	data := db.Bytes()
	signHash58(data, "000A2700138A4A0D")
	os.MkdirAll(filepath.Join(root, "iPod_Control", "iTunes"), 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesDB"), data, 0o644)
	d, err := Open(root, "")
	if err != nil || !strings.Contains(d.ReadOnly, "FireWire") {
		t.Fatalf("signed database without an ID: %v %q", err, d.ReadOnly)
	}
	os.WriteFile(filepath.Join(root, "iPod_Control", "Device", "SysInfo"), []byte("FirewireGuid: 0x000A2700138A4A0D\n"), 0o644)
	if d, _ = Open(root, ""); d.ReadOnly != "" {
		t.Fatalf("with an ID: %q", d.ReadOnly)
	}
}

// The 5th generation nano keeps a compressed iTunesCDB: shown, read-only.
func TestCompressedDB(t *testing.T) {
	root := fakeIPod(t, "ModelNumStr: xC060\n")
	db := itdb.New("Nano")
	tr := db.NewTrack(itdb.NewTrackInfo{Title: "Tune", Location: ":iPod_Control:Music:F00:A.mp3", FileTypeCode: "MP3 ", Kind: "MPEG audio file"})
	_ = tr
	raw := db.Bytes()
	hl := int(binary.LittleEndian.Uint32(raw[4:]))
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(raw[hl:])
	zw.Close()
	c := append(append([]byte(nil), raw[:hl]...), z.Bytes()...)
	c[0xA8] = 1
	binary.LittleEndian.PutUint32(c[8:], uint32(len(c)))
	os.MkdirAll(filepath.Join(root, "iPod_Control", "iTunes"), 0o755)
	os.WriteFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesCDB"), c, 0o644)
	d, err := Open(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.DB.Tracks) != 1 || d.DB.Tracks[0].Title() != "Tune" || d.ReadOnly == "" {
		t.Fatalf("compressed: %d tracks, %q", len(d.DB.Tracks), d.ReadOnly)
	}
	if err := d.Save(); err == nil {
		t.Fatal("saved a read-only iPod")
	}
}

// Shuffles: the iTunesSD written from libgpod's database matches libgpod's.
func TestShuffleMatchesLibgpod(t *testing.T) {
	dir := os.Getenv("LIBGPOD_HASH58_DIR")
	if dir == "" {
		t.Skip("LIBGPOD_HASH58_DIR not set")
	}
	for _, model := range []string{"xA546", "xB867", "xC584"} {
		root := t.TempDir()
		if out, err := exec.Command("cp", "-a", filepath.Join(dir, model, "iPod_Control"), root).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		want, _ := os.ReadFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesSD"))
		os.WriteFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesStats"), []byte{0, 0, 0}, 0o644)
		d, err := Open(root, "")
		if err != nil || d.ReadOnly != "" || !d.Model.Shuffle {
			t.Fatalf("%s: %v %q %+v", model, err, d.ReadOnly, d.Model)
		}
		if err := d.Save(); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(filepath.Join(root, "iPod_Control", "iTunes", "iTunesSD"))
		if model == "xA546" {
			// The old format's "play in shuffle mode" flag: libgpod writes the
			// iTunesDB's skip flag as it is (0 for every song), which by the
			// format's description would leave songs out of shuffle mode;
			// iTunes and rebuild_db write 1 for songs. Everything else matches.
			for i := 18 + 0x22B; i < len(got); i += 558 {
				if got[i] != 1 {
					t.Fatalf("shuffle flag at 0x%x: %d", i, got[i])
				}
				got[i] = want[i]
			}
		}
		if !bytes.Equal(got, want) {
			for i := range got {
				if i >= len(want) || got[i] != want[i] {
					t.Fatalf("%s: iTunesSD differs at 0x%x (%d vs %d bytes)", model, i, len(got), len(want))
				}
			}
			t.Fatalf("%s: lengths %d vs %d", model, len(got), len(want))
		}
		if _, err := os.Stat(filepath.Join(root, "iPod_Control", "iTunes", "iTunesStats")); err == nil {
			t.Fatal("stale iTunesStats kept")
		}
	}
}

// Play counts from the shuffle's iTunesStats.
func TestShuffleStats(t *testing.T) {
	db := itdb.New("S")
	for _, n := range []string{"a", "b"} {
		db.NewTrack(itdb.NewTrackInfo{Title: n, Location: ":iPod_Control:Music:F00:" + n + ".mp3"})
	}
	// old format: 3-byte count, 3 bytes, then 18-byte little-endian entries
	v1 := []byte{2, 0, 0, 0, 0, 0}
	for _, plays := range []byte{3, 0} {
		e := make([]byte, 18)
		e[0] = 18
		e[12] = plays
		v1 = append(v1, e...)
	}
	if !mergeShuffleStats(db, v1, true) || db.Tracks[0].PlayCount() != 3 || db.Tracks[1].PlayCount() != 0 {
		t.Fatal("v1 stats")
	}
	v2 := make([]byte, 8)
	v2[0] = 2
	for _, plays := range []uint32{1, 5} {
		e := make([]byte, 32)
		e[0] = 32
		binary.LittleEndian.PutUint32(e[8:], plays)
		v2 = append(v2, e...)
	}
	if !mergeShuffleStats(db, v2, false) || db.Tracks[0].PlayCount() != 4 || db.Tracks[1].PlayCount() != 5 {
		t.Fatal("v2 stats")
	}
	if mergeShuffleStats(db, v2[:8+32], false) {
		t.Fatal("truncated stats accepted")
	}
}
