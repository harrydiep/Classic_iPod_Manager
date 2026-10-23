package convert

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"podmanager/internal/media"
)

// Job is one song to convert.
type Job struct {
	Src     string            // source file
	Info    *media.SourceInfo // from media.Inspect(Src)
	Format  string            // output format ID
	Quality string            // quality ID ("" = the format's default)
	ForIPod bool              // keep within what an iPod plays (16-bit, ≤48 kHz, stereo)
	Tags    media.Tags        // tags for the new file
	Art     media.ArtEdit     // cover for the new file; inactive = copy the source's
	Out     string            // where the new file goes
	WorkDir string            // folder for temporary files ("" = system temp)
}

// ErrNoDecoder means the source format needs a decoder that isn't there.
type ErrNoDecoder struct {
	Label string
	Dir   string
}

func (e ErrNoDecoder) Error() string {
	return fmt.Sprintf("Classic iPod Manager can't read %s files by itself – put ffmpeg.exe in %s", e.Label, e.Dir)
}

// Plan is what a conversion will do, for showing before it starts.
type plan struct {
	format     Format
	quality    string
	bits       int // output bits for lossless/PCM formats
	rate       int // 0 = keep
	channels   int // 0 = keep
	encoder    *Tool
	needDecode bool
}

func (c Config) plan(j Job) (plan, error) {
	f, ok := FormatByID(j.Format)
	if !ok {
		return plan{}, fmt.Errorf("unknown format %q", j.Format)
	}
	p := plan{format: f, quality: j.Quality, bits: 16}
	if p.quality == "" || !hasQuality(f, p.quality) {
		for _, q := range f.Qualities {
			if q.Default {
				p.quality = q.ID
			}
		}
	}
	si := j.Info
	if f.Lossless && !j.ForIPod && si.Bits > 16 {
		p.bits = 24
	}
	if (j.ForIPod || f.ID == "mp3") && si.SampleRate > 48000 {
		if si.SampleRate%48000 == 0 {
			p.rate = 48000
		} else {
			p.rate = 44100
		}
	}
	if (j.ForIPod || f.ID == "mp3") && si.Channels > 2 {
		p.channels = 2
	}
	enc, err := c.Encoder(f.ID)
	if err != nil {
		return p, err
	}
	p.encoder = enc
	return p, nil
}

func hasQuality(f Format, id string) bool {
	for _, q := range f.Qualities {
		if q.ID == id {
			return true
		}
	}
	return false
}

// Check reports whether a job can run (encoder and decoder present).
func (c Config) Check(j Job) error {
	p, err := c.plan(j)
	if err != nil {
		return err
	}
	if p.encoder != nil && p.encoder.Kind == KindFFmpeg {
		return nil
	}
	_, err = c.decodePlan(j, p)
	return err
}

// Run converts one song.
func (c Config) Run(ctx context.Context, j Job) error {
	p, err := c.plan(j)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(j.WorkDir, "podmanager-convert-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	out := "out" + p.format.Ext
	outPath := filepath.Join(work, out)

	switch {
	case p.encoder != nil && p.encoder.Kind == KindFFmpeg:
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", j.Src,
			"-map", "0:a:0", "-vn", "-sn", "-map_metadata", "-1"}
		args = append(args, shapeArgs(p)...)
		args = append(args, ffmpegCodecArgs(p)...)
		args = append(args, out)
		if err := run(ctx, p.encoder, work, args); err != nil {
			return err
		}
	default:
		wav, err := c.decode(ctx, j, p, work)
		if err != nil {
			return err
		}
		switch {
		case p.format.ID == "wav":
			if err := transcodePCM(wav, outPath, false, p.bits); err != nil {
				return err
			}
		case p.format.ID == "aiff":
			if err := transcodePCM(wav, outPath, true, p.bits); err != nil {
				return err
			}
		default:
			args := encodeArgs(p, filepath.Base(wav), out)
			if err := run(ctx, p.encoder, work, args); err != nil {
				return err
			}
		}
	}
	if st, err := os.Stat(outPath); err != nil || st.Size() == 0 {
		return errors.New("the encoder didn't produce a file")
	}
	if p.format.Tags {
		art := j.Art
		if art.Set == nil && !art.Remove {
			if pic, _ := media.ReadArtwork(j.Src); pic != nil {
				art.Set = pic
			}
		}
		if err := media.WriteTagsArt(outPath, j.Tags, art); err != nil {
			return fmt.Errorf("couldn't tag the new file: %w", err)
		}
	}
	si, err := media.Inspect(outPath)
	if err != nil {
		return fmt.Errorf("the new file can't be read: %w", err)
	}
	if j.Info.LengthMs > 2000 && (si.LengthMs == 0 || si.LengthMs+1500 < j.Info.LengthMs) {
		return fmt.Errorf("the new file is shorter than the original (%.1fs instead of %.1fs)", float64(si.LengthMs)/1000, float64(j.Info.LengthMs)/1000)
	}
	if err := os.MkdirAll(filepath.Dir(j.Out), 0o755); err != nil {
		return err
	}
	return moveFile(outPath, j.Out)
}

// shapeArgs are FFmpeg options for sample rate, channels and bit depth.
func shapeArgs(p plan) []string {
	var a []string
	if p.rate > 0 {
		a = append(a, "-ar", strconv.Itoa(p.rate))
	}
	if p.channels > 0 {
		a = append(a, "-ac", strconv.Itoa(p.channels))
	}
	return a
}

func ffmpegCodecArgs(p plan) []string {
	q := p.quality
	switch p.format.ID {
	case "mp3":
		if strings.HasPrefix(q, "v") {
			return []string{"-c:a", "libmp3lame", "-q:a", q[1:]}
		}
		return []string{"-c:a", "libmp3lame", "-b:a", strings.TrimPrefix(q, "cbr") + "k", "-id3v2_version", "3"}
	case "aac":
		return []string{"-c:a", "aac", "-b:a", strings.TrimPrefix(q, "cbr") + "k"}
	case "alac":
		if p.bits > 16 {
			return []string{"-c:a", "alac", "-sample_fmt", "s32p"}
		}
		return []string{"-c:a", "alac", "-sample_fmt", "s16p"}
	case "flac":
		lvl := strings.TrimPrefix(q, "l")
		if p.bits > 16 {
			return []string{"-c:a", "flac", "-compression_level", lvl, "-sample_fmt", "s32"}
		}
		return []string{"-c:a", "flac", "-compression_level", lvl, "-sample_fmt", "s16"}
	case "vorbis":
		return []string{"-c:a", "libvorbis", "-q:a", strings.TrimPrefix(q, "q")}
	case "wav":
		return []string{"-c:a", pcmCodec(p.bits, false)}
	case "aiff":
		return []string{"-c:a", pcmCodec(p.bits, true)}
	}
	return nil
}

func pcmCodec(bits int, be bool) string {
	end := "le"
	if be {
		end = "be"
	}
	if bits > 16 {
		return "pcm_s24" + end
	}
	return "pcm_s16" + end
}

// encodeArgs builds the command line for a dedicated encoder, which reads
// a WAV file and writes the output (both in the working folder).
func encodeArgs(p plan, in, out string) []string {
	q := p.quality
	kb := strings.TrimPrefix(q, "cbr")
	switch p.encoder.Kind {
	case KindLame:
		a := []string{"--silent", "--noreplaygain", "-q", "2"}
		if strings.HasPrefix(q, "v") {
			a = append(a, "-V", q[1:])
		} else {
			a = append(a, "-b", kb, "--cbr")
		}
		return append(a, in, out)
	case KindQaac:
		if p.format.ID == "alac" {
			return []string{"--silent", "-A", "-o", out, in}
		}
		return []string{"--silent", "--cvbr", kb, "-q", "2", "-o", out, in}
	case KindRefalac:
		return []string{"--silent", "-o", out, in}
	case KindFdkaac:
		return []string{"-S", "-m", "0", "-b", kb + "000", "-o", out, in}
	case KindNero:
		return []string{"-ignorelength", "-br", kb + "000", "-if", in, "-of", out}
	case KindFlac:
		return []string{"--silent", "-f", "-" + strings.TrimPrefix(q, "l"), "-o", out, in}
	case KindOggenc:
		return []string{"--quiet", "-q", strings.TrimPrefix(q, "q"), "-o", out, in}
	}
	return nil
}

type decodeStep struct {
	builtin bool
	tool    *Tool
}

func (c Config) decodePlan(j Job, p plan) (decodeStep, error) {
	si := j.Info
	pcm := (si.Format == "wav" || si.Format == "aiff") && si.Bits > 0
	if pcm && p.rate == 0 && p.channels == 0 {
		return decodeStep{builtin: true}, nil
	}
	if ff := c.ffmpeg(); ff != nil {
		return decodeStep{tool: ff}, nil
	}
	if p.rate > 0 || p.channels > 0 {
		return decodeStep{}, fmt.Errorf("changing the sample rate or number of channels needs ffmpeg.exe in %s", c.Dir("decoder"))
	}
	t := c.decoderFor(si.Format)
	if t == nil {
		return decodeStep{}, ErrNoDecoder{Label: si.Label, Dir: c.Dir("decoder")}
	}
	return decodeStep{tool: t}, nil
}

// decode produces a WAV file of the source in the working folder with the
// planned bit depth, sample rate and channels.
func (c Config) decode(ctx context.Context, j Job, p plan, work string) (string, error) {
	step, err := c.decodePlan(j, p)
	if err != nil {
		return "", err
	}
	dec := filepath.Join(work, "dec.wav")
	if step.builtin {
		return dec, transcodePCM(j.Src, dec, false, p.bits)
	}
	t := step.tool
	if t.Kind == KindFFmpeg {
		args := []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-i", j.Src, "-map", "0:a:0", "-vn", "-sn", "-map_metadata", "-1"}
		args = append(args, shapeArgs(p)...)
		args = append(args, "-c:a", pcmCodec(p.bits, false), "-f", "wav", "dec.wav")
		return dec, run(ctx, t, work, args)
	}
	src, err := stage(j.Src, work)
	if err != nil {
		return "", err
	}
	raw := "raw.wav"
	var args []string
	switch t.Kind {
	case KindLame:
		args = []string{"--silent", "--decode", src, raw}
	case KindFlac:
		args = []string{"--silent", "-d", "-f", "-o", raw, src}
	case KindQaac, KindRefalac:
		args = []string{"--silent", "-D", "-o", raw, src}
	case KindOggdec:
		args = []string{"--quiet", "-o", raw, src}
	default:
		return "", fmt.Errorf("%s can't decode %s", t.Name, j.Info.Label)
	}
	if err := run(ctx, t, work, args); err != nil {
		return "", err
	}
	// match the planned bit depth (decoders keep the source's)
	if err := transcodePCM(filepath.Join(work, raw), dec, false, p.bits); err != nil {
		return "", fmt.Errorf("couldn't read the decoded audio: %w", err)
	}
	os.Remove(filepath.Join(work, raw))
	return dec, nil
}

// stage makes the source reachable under a plain ASCII name in the working
// folder, for command-line tools that can't open other file names.
func stage(src, work string) (string, error) {
	name := "src" + strings.ToLower(filepath.Ext(src))
	if isASCII(src) {
		return src, nil
	}
	dst := filepath.Join(work, name)
	if err := os.Link(src, dst); err == nil {
		return name, nil
	}
	if err := copyFile(src, dst); err != nil {
		return "", err
	}
	return name, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// run starts a tool in the working folder and waits for it.
func run(ctx context.Context, t *Tool, dir string, args []string) error {
	cmd := exec.CommandContext(ctx, t.Path, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &limitWriter{w: &out, n: 64 << 10}
	cmd.Stderr = cmd.Stdout
	hideWindow(cmd)
	cmd.WaitDelay = 3 * time.Second // don't hang on a child process holding the output open
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(lastLines(out.String(), 3))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s failed: %s", t.Name, msg)
	}
	return nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := len(p)
		if k > l.n {
			k = l.n
		}
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(s, "\r", "\n")), "\n")
	var keep []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			keep = append(keep, strings.TrimSpace(l))
		}
	}
	if len(keep) > n {
		keep = keep[len(keep)-n:]
	}
	return strings.Join(keep, " ")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.CopyBuffer(out, in, make([]byte, 1<<20)); err != nil {
		out.Close()
		os.Remove(dst)
		return err
	}
	return out.Close()
}

// moveFile renames, or copies across drives.
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	tmp := dst + ".podmanager-tmp"
	if err := copyFile(src, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	os.Remove(src)
	return nil
}
