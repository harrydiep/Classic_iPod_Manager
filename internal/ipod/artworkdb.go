package ipod

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"podmanager/internal/itdb"
)

// Writing album art: each song with a cover has an image entry (mhii) in
// iPod_Control/Artwork/ArtworkDB whose song ID is the track's database ID.
// The entry lists one thumbnail per format; the pixels live in
// F<format>_<n>.ithmb files as RGB565 little-endian. Each generation uses
// its own set of square sizes (model.go): the iPod Video 100×100 (format
// 1028) and 200×200 (1029), the classic 320, 128 and 56, and so on. The
// layout matches what libgpod writes.

type artThumb struct {
	format     uint32
	file       string // ":F1029_1.ithmb"
	offset     uint32
	size       uint32
	w, h       int
	padX, padY int
	hdr        []byte // original mhni header, if read from disk
}

type artEntry struct {
	header []byte // mhii header, kept for fields we don't manage
	id     uint32
	song   uint64
	thumbs []artThumb
	other  [][]byte // child mhods that aren't thumbnails
}

type artDB struct {
	mhfd    []byte // header
	entries []*artEntry
	albums  []byte   // mhsd type 2 payload, kept as is
	extra   [][]byte // whole mhsd sections of other types, kept as is
	nextID  uint32
}

const artMinID = 100

func (d *Device) artDir() string    { return filepath.Join(d.Root, "iPod_Control", "Artwork") }
func (d *Device) artDBPath() string { return filepath.Join(d.artDir(), "ArtworkDB") }

// canWriteArtwork reports whether Classic iPod Manager knows this iPod's cover formats.
func (d *Device) CanWriteArtwork() bool { return d.canWriteArtwork() }

func (d *Device) canWriteArtwork() bool { return len(d.coverFormats()) > 0 }

// coverFormats is the model's list, or for an iPod the model table doesn't
// know, the square formats its ArtworkDB already uses.
func (d *Device) coverFormats() []ArtFormat {
	if d.Model.known || d.Model.Shuffle {
		return d.Model.art
	}
	d.unknownArtOnce.Do(func() {
		b, err := os.ReadFile(d.artDBPath())
		if err != nil {
			return
		}
		db, err := parseArtDBFull(b)
		if err != nil {
			return
		}
		seen := map[uint32]bool{}
		for _, e := range db.entries {
			for _, t := range e.thumbs {
				f, ok := thumbFormats[t.format]
				if ok && f[0] == f[1] && !seen[t.format] && int(t.size) == f[0]*f[1]*2 {
					seen[t.format] = true
					d.unknownArt = append(d.unknownArt, ArtFormat{t.format, f[0]})
				}
			}
		}
	})
	return d.unknownArt
}

func (d *Device) loadArtDB() (*artDB, error) {
	if d.artw != nil {
		return d.artw, nil
	}
	b, err := os.ReadFile(d.artDBPath())
	if errors.Is(err, os.ErrNotExist) {
		d.artw = &artDB{nextID: artMinID}
		return d.artw, nil
	}
	if err != nil {
		return nil, err
	}
	db, err := parseArtDBFull(b)
	if err != nil {
		return nil, fmt.Errorf("the iPod's artwork database can't be read: %w", err)
	}
	d.artw = db
	return db, nil
}

func parseArtDBFull(b []byte) (*artDB, error) {
	le := binary.LittleEndian
	if len(b) < 0x20 || string(b[:4]) != "mhfd" {
		return nil, errors.New("not an ArtworkDB")
	}
	hl := int(le.Uint32(b[4:]))
	if hl > len(b) || hl < 0x20 {
		return nil, errors.New("bad mhfd header")
	}
	// mhfd+0x1C holds the last image ID handed out (libgpod's "next_id").
	db := &artDB{mhfd: append([]byte(nil), b[:hl]...), nextID: le.Uint32(b[0x1C:]) + 1}
	off := hl
	for off+16 <= len(b) && string(b[off:off+4]) == "mhsd" {
		shl := int(le.Uint32(b[off+4:]))
		stl := int(le.Uint32(b[off+8:]))
		if stl < shl || off+stl > len(b) {
			return nil, errors.New("bad mhsd")
		}
		typ := le.Uint16(b[off+12:])
		body := b[off+shl : off+stl]
		switch typ {
		case 1:
			if err := db.parseImages(body); err != nil {
				return nil, err
			}
		case 2:
			db.albums = append([]byte(nil), body...)
		case 3: // file list: rebuilt from the thumbnails
		default:
			db.extra = append(db.extra, append([]byte(nil), b[off:off+stl]...))
		}
		off += stl
	}
	for _, e := range db.entries {
		if e.id >= db.nextID {
			db.nextID = e.id + 1
		}
	}
	if db.nextID < artMinID {
		db.nextID = artMinID
	}
	return db, nil
}

func (db *artDB) parseImages(b []byte) error {
	le := binary.LittleEndian
	if len(b) < 12 || string(b[:4]) != "mhli" {
		return errors.New("bad mhli")
	}
	n := int(le.Uint32(b[8:]))
	off := int(le.Uint32(b[4:]))
	for i := 0; i < n; i++ {
		if off+0x20 > len(b) || string(b[off:off+4]) != "mhii" {
			return errors.New("bad mhii")
		}
		hl := int(le.Uint32(b[off+4:]))
		tl := int(le.Uint32(b[off+8:]))
		if hl < 0x1C || tl < hl || off+tl > len(b) {
			return errors.New("bad mhii size")
		}
		e := &artEntry{header: append([]byte(nil), b[off:off+hl]...), id: le.Uint32(b[off+0x10:]), song: le.Uint64(b[off+0x14:])}
		for c := off + hl; c+24 <= off+tl; {
			if string(b[c:c+4]) != "mhod" {
				break
			}
			chl := int(le.Uint32(b[c+4:]))
			ctl := int(le.Uint32(b[c+8:]))
			if ctl < chl || c+ctl > off+tl {
				return errors.New("bad mhod")
			}
			if le.Uint16(b[c+12:]) == 2 && c+chl+36 <= c+ctl && string(b[c+chl:c+chl+4]) == "mhni" {
				if r, ok := parseMhni(b[c+chl : c+ctl]); ok {
					mh := b[c+chl:]
					nhl := int(le.Uint32(mh[4:]))
					var hdr []byte
					if nhl >= 0x24 && nhl <= len(mh) {
						hdr = append([]byte(nil), mh[:nhl]...)
					}
					e.thumbs = append(e.thumbs, artThumb{format: r.format, file: r.file, offset: r.offset, size: r.size,
						w: r.width, h: r.height, padX: r.padX, padY: r.padY, hdr: hdr})
				}
			} else {
				e.other = append(e.other, append([]byte(nil), b[c:c+ctl]...))
			}
			c += ctl
		}
		db.entries = append(db.entries, e)
		off += tl
	}
	return nil
}

// ---------------------------------------------------------------- writing

func artString(s string) []byte {
	u := utf16.Encode([]rune(s))
	body := make([]byte, 12+len(u)*2)
	binary.LittleEndian.PutUint32(body[0:], uint32(len(u)*2))
	binary.LittleEndian.PutUint32(body[4:], 2) // UTF-16LE
	for i, c := range u {
		binary.LittleEndian.PutUint16(body[12+2*i:], c)
	}
	for len(body)%4 != 0 {
		body = append(body, 0)
	}
	return artChunk("mhod", 0x18, func(h []byte) { binary.LittleEndian.PutUint16(h[12:], 3) }, body)
}

// artChunk builds a chunk: tag, header length, total length, header fields
// set by fill, then the body.
func artChunk(tag string, hl int, fill func(h []byte), body []byte) []byte {
	h := make([]byte, hl, hl+len(body))
	copy(h, tag)
	binary.LittleEndian.PutUint32(h[4:], uint32(hl))
	binary.LittleEndian.PutUint32(h[8:], uint32(hl+len(body)))
	if fill != nil {
		fill(h)
	}
	return append(h, body...)
}

func (db *artDB) bytes() []byte {
	le := binary.LittleEndian
	var images bytes.Buffer
	formats := map[uint32]uint32{}
	for _, e := range db.entries {
		var kids bytes.Buffer
		for _, t := range e.thumbs {
			formats[t.format] = t.size
			nhl := 0x4C
			if len(t.hdr) > nhl {
				nhl = len(t.hdr)
			}
			mhni := artChunk("mhni", nhl, func(h []byte) {
				if len(t.hdr) > 12 {
					copy(h[12:], t.hdr[12:])
				}
				le.PutUint32(h[0x0C:], 1)
				le.PutUint32(h[0x10:], t.format)
				le.PutUint32(h[0x14:], t.offset)
				le.PutUint32(h[0x18:], t.size)
				le.PutUint16(h[0x1C:], uint16(int16(t.padY)))
				le.PutUint16(h[0x1E:], uint16(int16(t.padX)))
				le.PutUint16(h[0x20:], uint16(t.h))
				le.PutUint16(h[0x22:], uint16(t.w))
			}, artString(t.file))
			kids.Write(artChunk("mhod", 0x18, func(h []byte) {
				// For container mhods the "total length" covers the child.
				le.PutUint16(h[12:], 2)
			}, mhni))
		}
		for _, o := range e.other {
			kids.Write(o)
		}
		hl := len(e.header)
		if hl < 0x98 {
			hl = 0x98
		}
		images.Write(artChunk("mhii", hl, func(h []byte) {
			copy(h[12:], e.header[min(12, len(e.header)):])
			le.PutUint32(h[0x0C:], uint32(len(e.thumbs)+len(e.other)))
			le.PutUint32(h[0x10:], e.id)
			le.PutUint64(h[0x14:], e.song)
		}, kids.Bytes()))
	}
	mhli := artChunk("mhli", 0x5C, nil, images.Bytes())
	le.PutUint32(mhli[8:], uint32(len(db.entries))) // mhli: count instead of length
	albums := db.albums
	if len(albums) == 0 {
		albums = artChunk("mhla", 0x5C, nil, nil)
		le.PutUint32(albums[8:], 0)
	}
	var files bytes.Buffer
	ids := make([]int, 0, len(formats))
	for id := range formats {
		ids = append(ids, int(id))
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	for _, id := range ids {
		files.Write(artChunk("mhif", 0x7C, func(h []byte) {
			le.PutUint32(h[0x10:], uint32(id))
			le.PutUint32(h[0x14:], formats[uint32(id)])
		}, nil))
	}
	mhlf := artChunk("mhlf", 0x5C, nil, files.Bytes())
	le.PutUint32(mhlf[8:], uint32(len(ids)))
	sec := func(typ uint16, body []byte) []byte {
		return artChunk("mhsd", 0x60, func(h []byte) { le.PutUint16(h[12:], typ) }, body)
	}
	body := append(append(sec(1, mhli), sec(2, albums)...), sec(3, mhlf)...)
	for _, x := range db.extra {
		body = append(body, x...)
	}
	hl := len(db.mhfd)
	if hl < 0x84 {
		hl = 0x84
	}
	return artChunk("mhfd", hl, func(h []byte) {
		if len(db.mhfd) >= 12 {
			copy(h[12:], db.mhfd[12:])
		} else {
			le.PutUint32(h[0x10:], 2)
			le.PutUint32(h[0x30:], 2)
		}
		le.PutUint32(h[0x14:], uint32(3+len(db.extra)))
		last := db.nextID - 1
		if last < artMinID {
			last = artMinID
		}
		le.PutUint32(h[0x1C:], last)
	}, body)
}

// ---------------------------------------------------------------- thumbnails

// coverThumb crops the picture to a centred square and scales it to side×side,
// returning RGB565 little-endian pixels.
func coverThumb(img image.Image, side int) []byte {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	s := w
	if h < s {
		s = h
	}
	x0, y0 := b.Min.X+(w-s)/2, b.Min.Y+(h-s)/2
	out := make([]byte, side*side*2)
	scale := float64(s) / float64(side)
	for y := 0; y < side; y++ {
		sy0 := float64(y) * scale
		sy1 := sy0 + scale
		for x := 0; x < side; x++ {
			sx0 := float64(x) * scale
			sx1 := sx0 + scale
			r, g, bl := areaAverage(img, x0, y0, sx0, sy0, sx1, sy1, scale < 1)
			v := uint16(r>>3)<<11 | uint16(g>>2)<<5 | uint16(bl>>3)
			binary.LittleEndian.PutUint16(out[(y*side+x)*2:], v)
		}
	}
	return out
}

// areaAverage averages the source pixels covering [sx0,sx1)×[sy0,sy1)
// (relative to x0,y0). When enlarging it samples bilinearly instead.
func areaAverage(img image.Image, x0, y0 int, sx0, sy0, sx1, sy1 float64, enlarge bool) (uint8, uint8, uint8) {
	if enlarge {
		cx, cy := (sx0+sx1)/2-0.5, (sy0+sy1)/2-0.5
		ix, iy := int(cx), int(cy)
		fx, fy := cx-float64(ix), cy-float64(iy)
		if cx < 0 {
			ix, fx = 0, 0
		}
		if cy < 0 {
			iy, fy = 0, 0
		}
		var acc [3]float64
		for dy := 0; dy < 2; dy++ {
			for dx := 0; dx < 2; dx++ {
				wgt := (1 - fx) * (1 - fy)
				if dx == 1 {
					wgt = fx * (1 - fy)
				}
				if dy == 1 {
					wgt = (1 - fx) * fy
					if dx == 1 {
						wgt = fx * fy
					}
				}
				c := rgbAt(img, x0+ix+dx, y0+iy+dy)
				acc[0] += wgt * float64(c.R)
				acc[1] += wgt * float64(c.G)
				acc[2] += wgt * float64(c.B)
			}
		}
		return uint8(acc[0] + 0.5), uint8(acc[1] + 0.5), uint8(acc[2] + 0.5)
	}
	var acc [3]float64
	var total float64
	for y := int(sy0); float64(y) < sy1; y++ {
		wy := min(float64(y+1), sy1) - max(float64(y), sy0)
		for x := int(sx0); float64(x) < sx1; x++ {
			wx := min(float64(x+1), sx1) - max(float64(x), sx0)
			wgt := wx * wy
			c := rgbAt(img, x0+x, y0+y)
			acc[0] += wgt * float64(c.R)
			acc[1] += wgt * float64(c.G)
			acc[2] += wgt * float64(c.B)
			total += wgt
		}
	}
	if total == 0 {
		return 0, 0, 0
	}
	return uint8(acc[0]/total + 0.5), uint8(acc[1]/total + 0.5), uint8(acc[2]/total + 0.5)
}

func rgbAt(img image.Image, x, y int) color.RGBA {
	b := img.Bounds()
	if x >= b.Max.X {
		x = b.Max.X - 1
	}
	if y >= b.Max.Y {
		y = b.Max.Y - 1
	}
	r, g, bl, a := img.At(x, y).RGBA()
	// composite transparent pixels over white
	if a < 0xFFFF {
		r = r + (0xFFFF - a)
		g = g + (0xFFFF - a)
		bl = bl + (0xFFFF - a)
	}
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255}
}

// ---------------------------------------------------------------- public API

// SetArtwork gives a track a cover on the iPod from picture data (JPEG, PNG
// or GIF). The thumbnails are written straight away; the database entries
// are written by Save.
func (d *Device) SetArtwork(t *itdb.Track, picture []byte) error {
	if d.ReadOnly != "" {
		return errors.New(d.ReadOnly)
	}
	if !d.canWriteArtwork() {
		return errors.New("this iPod doesn't show album art")
	}
	img, _, err := image.Decode(bytes.NewReader(picture))
	if err != nil {
		return fmt.Errorf("the picture can't be read: %w", err)
	}
	if img.Bounds().Dx() < 1 || img.Bounds().Dy() < 1 {
		return errors.New("the picture is empty")
	}
	db, err := d.loadArtDB()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.artDir(), 0o755); err != nil {
		return err
	}
	var thumbs []artThumb
	for _, f := range d.coverFormats() {
		px := coverThumb(img, f.Side)
		name, off, err := d.appendIthmb(db, f.ID, px)
		if err != nil {
			return err
		}
		thumbs = append(thumbs, artThumb{format: f.ID, file: ":" + name, offset: off, size: uint32(len(px)), w: f.Side, h: f.Side})
	}
	dbid := t.DBID()
	var e *artEntry
	for _, x := range db.entries {
		if x.song == dbid {
			e = x
		}
	}
	if e == nil {
		e = &artEntry{id: db.nextID, song: dbid}
		db.nextID++
		db.entries = append(db.entries, e)
	}
	if len(e.header) < 0x98 {
		h := make([]byte, 0x98)
		copy(h, e.header)
		e.header = h
	}
	binary.LittleEndian.PutUint32(e.header[0x28:], itdb.MacTime(time.Now()))
	binary.LittleEndian.PutUint32(e.header[0x30:], uint32(len(picture)))
	e.thumbs = thumbs
	t.SetArtwork(e.id, uint32(len(picture)))
	d.artDirty = true
	d.resetArtIndex()
	return nil
}

// RemoveArtwork takes a track's cover off the iPod.
func (d *Device) RemoveArtwork(t *itdb.Track) error {
	if d.ReadOnly != "" {
		return errors.New(d.ReadOnly)
	}
	db, err := d.loadArtDB()
	if err != nil {
		return err
	}
	dbid := t.DBID()
	kept := db.entries[:0]
	for _, e := range db.entries {
		if e.song != dbid {
			kept = append(kept, e)
		}
	}
	db.entries = kept
	t.ClearArtwork()
	d.artDirty = true
	d.resetArtIndex()
	return nil
}

func (d *Device) resetArtIndex() {
	idx := map[uint64][]thumbRef{}
	if d.artw != nil {
		for _, e := range d.artw.entries {
			for _, t := range e.thumbs {
				idx[e.song] = append(idx[e.song], thumbRef{format: t.format, file: t.file, offset: t.offset, size: t.size,
					width: t.w, height: t.h, padX: t.padX, padY: t.padY})
			}
		}
	}
	d.artOnce.Do(func() {})
	d.art = idx
}

// ithmbName parses "F1029_3.ithmb" into its format and number.
func ithmbName(name string) (format uint32, n int, ok bool) {
	name = strings.TrimPrefix(name, ":")
	if !strings.HasPrefix(name, "F") || !strings.HasSuffix(strings.ToLower(name), ".ithmb") {
		return 0, 0, false
	}
	f, num, found := strings.Cut(strings.TrimSuffix(name[1:], name[len(name)-6:]), "_")
	if !found {
		return 0, 0, false
	}
	fv, err1 := strconv.Atoi(f)
	nv, err2 := strconv.Atoi(num)
	return uint32(fv), nv, err1 == nil && err2 == nil
}

// currentIthmb returns the file new thumbnails of a format go into: the
// highest-numbered one in use or on disk, or F<format>_1.ithmb.
func (d *Device) currentIthmb(db *artDB, format uint32) string {
	best := 0
	consider := func(name string) {
		if f, n, ok := ithmbName(name); ok && f == format && n > best {
			best = n
		}
	}
	for _, e := range db.entries {
		for _, t := range e.thumbs {
			consider(t.file)
		}
	}
	if entries, err := os.ReadDir(d.artDir()); err == nil {
		for _, e := range entries {
			consider(e.Name())
		}
	}
	if best == 0 {
		best = 1
	}
	return fmt.Sprintf("F%d_%d.ithmb", format, best)
}

// appendIthmb appends pixels to the current .ithmb file for a format.
func (d *Device) appendIthmb(db *artDB, format uint32, px []byte) (string, uint32, error) {
	name := d.currentIthmb(db, format)
	p := filepath.Join(d.artDir(), name)
	f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return "", 0, err
	}
	// keep thumbnails aligned to whole slots
	if rem := end % int64(len(px)); rem != 0 {
		end += int64(len(px)) - rem
	}
	if end+int64(len(px)) > 0xFFFFFFFF {
		return "", 0, errors.New("the artwork file is full")
	}
	if _, err := f.WriteAt(px, end); err != nil {
		return "", 0, err
	}
	return name, uint32(end), f.Sync()
}

// saveArtwork writes the ArtworkDB if covers changed, dropping entries of
// songs that are gone and compacting .ithmb files with too much unused space.
func (d *Device) saveArtwork() error {
	if d.artw == nil {
		// Covers of deleted songs are cleaned up too, so look at the
		// database when there is one even if no cover was changed.
		if _, err := os.Stat(d.artDBPath()); err != nil || !d.canWriteArtwork() {
			return nil
		}
		if _, err := d.loadArtDB(); err != nil {
			return nil // leave a database we can't read alone
		}
	}
	db := d.artw
	alive := map[uint64]bool{}
	for _, t := range d.DB.Tracks {
		alive[t.DBID()] = true
	}
	kept := db.entries[:0]
	for _, e := range db.entries {
		if alive[e.song] {
			kept = append(kept, e)
		} else {
			d.artDirty = true
		}
	}
	db.entries = kept
	if !d.artDirty {
		return nil
	}
	if err := d.compactIthmbs(db); err != nil {
		return err
	}
	if err := d.backupArtwork(); err != nil {
		return err
	}
	if err := writeAtomic(d.artDBPath(), db.bytes()); err != nil {
		return err
	}
	d.removeUnusedIthmbs(db)
	d.artDirty = false
	d.resetArtIndex()
	return nil
}

func (d *Device) backupArtwork() error {
	if d.artBackedUp {
		return nil
	}
	data, err := os.ReadFile(d.artDBPath())
	if err == nil {
		if err := os.WriteFile(d.artDBPath()+".podmanager.bak", data, 0o644); err != nil {
			return err
		}
	}
	d.artBackedUp = true
	return nil
}

// compactIthmbs rewrites a format's thumbnails into a fresh file when more
// than a third of its current files is unused (and at least 256 KB).
func (d *Device) compactIthmbs(db *artDB) error {
	type fileUse struct{ live, size int64 }
	use := map[string]*fileUse{}
	for _, e := range db.entries {
		for _, t := range e.thumbs {
			name := strings.TrimPrefix(t.file, ":")
			if use[name] == nil {
				use[name] = &fileUse{}
				if st, err := os.Stat(filepath.Join(d.artDir(), name)); err == nil {
					use[name].size = st.Size()
				}
			}
			use[name].live += int64(t.size)
		}
	}
	byFormat := map[uint32][]string{}
	for name := range use {
		if f, _, ok := ithmbName(name); ok {
			byFormat[f] = append(byFormat[f], name)
		}
	}
	for format, names := range byFormat {
		var live, size int64
		for _, n := range names {
			live += use[n].live
			size += use[n].size
		}
		if size-live < 256<<10 || (size-live)*3 < size {
			continue
		}
		// next free file number
		maxN := 0
		for _, n := range names {
			if _, num, ok := ithmbName(n); ok && num > maxN {
				maxN = num
			}
		}
		if entries, err := os.ReadDir(d.artDir()); err == nil {
			for _, e := range entries {
				if f, num, ok := ithmbName(e.Name()); ok && f == format && num > maxN {
					maxN = num
				}
			}
		}
		newName := fmt.Sprintf("F%d_%d.ithmb", format, maxN+1)
		out, err := os.Create(filepath.Join(d.artDir(), newName))
		if err != nil {
			return err
		}
		var off int64
		srcs := map[string]*os.File{}
		fail := func(err error) error {
			out.Close()
			for _, f := range srcs {
				f.Close()
			}
			os.Remove(filepath.Join(d.artDir(), newName))
			return err
		}
		type move struct {
			t   *artThumb
			off int64
		}
		var moves []move
		for _, e := range db.entries {
			for i := range e.thumbs {
				t := &e.thumbs[i]
				if f, _, ok := ithmbName(t.file); !ok || f != format {
					continue
				}
				name := strings.TrimPrefix(t.file, ":")
				src := srcs[name]
				if src == nil {
					if src, err = os.Open(filepath.Join(d.artDir(), name)); err != nil {
						return fail(err)
					}
					srcs[name] = src
				}
				buf := make([]byte, t.size)
				if _, err := src.ReadAt(buf, int64(t.offset)); err != nil {
					return fail(err)
				}
				if _, err := out.WriteAt(buf, off); err != nil {
					return fail(err)
				}
				moves = append(moves, move{t, off})
				off += int64(t.size)
			}
		}
		if err := out.Sync(); err != nil {
			return fail(err)
		}
		out.Close()
		for _, f := range srcs {
			f.Close()
		}
		for _, m := range moves {
			m.t.file = ":" + newName
			m.t.offset = uint32(m.off)
		}
	}
	return nil
}

// removeUnusedIthmbs deletes .ithmb files of known cover formats that no
// entry refers to any more (after compaction). Only called once the new
// ArtworkDB is on disk.
func (d *Device) removeUnusedIthmbs(db *artDB) {
	used := map[string]bool{}
	for _, e := range db.entries {
		for _, t := range e.thumbs {
			used[strings.ToLower(strings.TrimPrefix(t.file, ":"))] = true
		}
	}
	entries, err := os.ReadDir(d.artDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		f, _, ok := ithmbName(e.Name())
		if !ok || used[strings.ToLower(e.Name())] {
			continue
		}
		for _, vf := range d.coverFormats() {
			if f == vf.ID {
				os.Remove(filepath.Join(d.artDir(), e.Name()))
			}
		}
	}
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".podmanager-new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
