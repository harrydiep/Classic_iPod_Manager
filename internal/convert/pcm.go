package convert

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// pcmInfo describes integer PCM audio inside a WAV or AIFF file.
type pcmInfo struct {
	channels int
	rate     int
	bits     int
	dataOff  int64
	dataLen  int64
	bigEnd   bool
}

// readWAVInfo parses a PCM WAV header (plain or WAVE_FORMAT_EXTENSIBLE).
func readWAVInfo(f *os.File) (pcmInfo, error) {
	var pi pcmInfo
	st, err := f.Stat()
	if err != nil {
		return pi, err
	}
	var h [12]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return pi, errors.New("not a WAV file")
	}
	gotFmt := false
	for off := int64(12); off+8 <= st.Size(); {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], off); err != nil {
			break
		}
		sz := int64(binary.LittleEndian.Uint32(ch[4:]))
		switch string(ch[:4]) {
		case "fmt ":
			b := make([]byte, 40)
			n, _ := f.ReadAt(b, off+8)
			if n < 16 {
				return pi, errors.New("bad WAV format chunk")
			}
			tag := binary.LittleEndian.Uint16(b)
			if tag == 0xFFFE && n >= 26 {
				tag = binary.LittleEndian.Uint16(b[24:]) // sub-format GUID starts with the format tag
			}
			if tag != 1 {
				return pi, errors.New("the WAV file isn't integer PCM")
			}
			pi.channels = int(binary.LittleEndian.Uint16(b[2:]))
			pi.rate = int(binary.LittleEndian.Uint32(b[4:]))
			pi.bits = int(binary.LittleEndian.Uint16(b[14:]))
			gotFmt = true
		case "data":
			pi.dataOff = off + 8
			pi.dataLen = sz
			if sz == 0 || sz == 0xFFFFFFFF || pi.dataOff+sz > st.Size() {
				pi.dataLen = st.Size() - pi.dataOff // streamed WAV with an unset size
			}
			if gotFmt {
				return pi, pi.check()
			}
		}
		off += 8 + sz + sz%2
	}
	if !gotFmt || pi.dataOff == 0 {
		return pi, errors.New("the WAV file has no audio")
	}
	return pi, pi.check()
}

func (pi pcmInfo) check() error {
	if pi.channels < 1 || pi.channels > 8 || pi.rate < 1000 || pi.rate > 768000 {
		return errors.New("unsupported WAV layout")
	}
	switch pi.bits {
	case 8, 16, 24, 32:
		return nil
	}
	return fmt.Errorf("unsupported bit depth %d", pi.bits)
}

// readAIFFInfo parses an uncompressed AIFF (or AIFC "NONE"/"sowt") header.
func readAIFFInfo(f *os.File) (pcmInfo, error) {
	var pi pcmInfo
	st, err := f.Stat()
	if err != nil {
		return pi, err
	}
	var h [12]byte
	if _, err := f.ReadAt(h[:], 0); err != nil || string(h[:4]) != "FORM" {
		return pi, errors.New("not an AIFF file")
	}
	aifc := string(h[8:12]) == "AIFC"
	pi.bigEnd = true
	for off := int64(12); off+8 <= st.Size(); {
		var ch [8]byte
		if _, err := f.ReadAt(ch[:], off); err != nil {
			break
		}
		sz := int64(binary.BigEndian.Uint32(ch[4:]))
		switch string(ch[:4]) {
		case "COMM":
			b := make([]byte, 22)
			f.ReadAt(b, off+8)
			pi.channels = int(binary.BigEndian.Uint16(b))
			pi.bits = int(binary.BigEndian.Uint16(b[6:]))
			pi.rate = int(math.Round(extendedFloat(b[8:18])))
			if aifc {
				switch string(b[18:22]) {
				case "NONE":
				case "sowt":
					pi.bigEnd = false
				default:
					return pi, errors.New("the AIFF file is compressed")
				}
			}
		case "SSND":
			var o [4]byte
			f.ReadAt(o[:], off+8)
			pi.dataOff = off + 16 + int64(binary.BigEndian.Uint32(o[:]))
			pi.dataLen = sz - 8 - int64(binary.BigEndian.Uint32(o[:]))
			if pi.dataOff+pi.dataLen > st.Size() {
				pi.dataLen = st.Size() - pi.dataOff
			}
		}
		off += 8 + sz + sz%2
	}
	if pi.dataOff == 0 || pi.channels == 0 {
		return pi, errors.New("the AIFF file has no audio")
	}
	return pi, pi.check()
}

func extendedFloat(b []byte) float64 {
	exp := int(binary.BigEndian.Uint16(b[0:]) & 0x7FFF)
	mant := binary.BigEndian.Uint64(b[2:])
	if exp == 0 && mant == 0 {
		return 0
	}
	return float64(mant) * math.Pow(2, float64(exp-16383-63))
}

func toExtended(v float64) [10]byte {
	var b [10]byte
	if v <= 0 {
		return b
	}
	exp := int(math.Floor(math.Log2(v)))
	mant := uint64(v / math.Pow(2, float64(exp-63)))
	binary.BigEndian.PutUint16(b[0:], uint16(exp+16383))
	binary.BigEndian.PutUint64(b[2:], mant)
	return b
}

// pcmWriter writes WAV or AIFF output.
type pcmFormat struct {
	aiff     bool
	channels int
	rate     int
	bits     int
}

func writePCMHeader(w io.Writer, pf pcmFormat, dataLen int64) error {
	block := pf.channels * pf.bits / 8
	if pf.aiff {
		frames := dataLen / int64(block)
		commLen := 18
		ssndLen := 8 + dataLen
		total := 4 + 8 + int64(commLen) + 8 + ssndLen + dataLen%2
		var b []byte
		b = append(b, "FORM"...)
		b = binary.BigEndian.AppendUint32(b, uint32(total))
		b = append(b, "AIFFCOMM"...)
		b = binary.BigEndian.AppendUint32(b, uint32(commLen))
		b = binary.BigEndian.AppendUint16(b, uint16(pf.channels))
		b = binary.BigEndian.AppendUint32(b, uint32(frames))
		b = binary.BigEndian.AppendUint16(b, uint16(pf.bits))
		ext := toExtended(float64(pf.rate))
		b = append(b, ext[:]...)
		b = append(b, "SSND"...)
		b = binary.BigEndian.AppendUint32(b, uint32(ssndLen))
		b = append(b, 0, 0, 0, 0, 0, 0, 0, 0)
		_, err := w.Write(b)
		return err
	}
	var b []byte
	b = append(b, "RIFF"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(36+dataLen+dataLen%2))
	b = append(b, "WAVEfmt "...)
	b = binary.LittleEndian.AppendUint32(b, 16)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, uint16(pf.channels))
	b = binary.LittleEndian.AppendUint32(b, uint32(pf.rate))
	b = binary.LittleEndian.AppendUint32(b, uint32(pf.rate*block))
	b = binary.LittleEndian.AppendUint16(b, uint16(block))
	b = binary.LittleEndian.AppendUint16(b, uint16(pf.bits))
	b = append(b, "data"...)
	b = binary.LittleEndian.AppendUint32(b, uint32(dataLen))
	_, err := w.Write(b)
	return err
}

// transcodePCM copies integer PCM from a WAV/AIFF source to a WAV/AIFF
// destination, changing byte order and reducing the bit depth to outBits
// (with TPDF dither when reducing). It can't change the sample rate or
// channel count.
func transcodePCM(src, dst string, toAIFF bool, outBits int) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	var magic [4]byte
	in.ReadAt(magic[:], 0)
	var pi pcmInfo
	if string(magic[:]) == "FORM" {
		pi, err = readAIFFInfo(in)
	} else {
		pi, err = readWAVInfo(in)
	}
	if err != nil {
		return err
	}
	if outBits == 0 || outBits > pi.bits {
		outBits = pi.bits
	}
	if outBits < 16 {
		outBits = 16
	}
	inBytes := pi.bits / 8
	outBytes := outBits / 8
	frames := pi.dataLen / int64(inBytes*pi.channels)
	dataLen := frames * int64(outBytes*pi.channels)
	if dataLen > 0xFFFFFFF0 {
		return errors.New("the audio is too long for a WAV/AIFF file")
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(out, 1<<20)
	fail := func(err error) error { out.Close(); os.Remove(dst); return err }
	if err := writePCMHeader(w, pcmFormat{aiff: toAIFF, channels: pi.channels, rate: pi.rate, bits: outBits}, dataLen); err != nil {
		return fail(err)
	}
	r := bufio.NewReaderSize(io.NewSectionReader(in, pi.dataOff, frames*int64(inBytes*pi.channels)), 1<<20)
	sample := make([]byte, inBytes)
	ob := make([]byte, outBytes)
	var seed uint32 = 0x9E3779B9
	rnd := func() float64 {
		seed ^= seed << 13
		seed ^= seed >> 17
		seed ^= seed << 5
		return float64(seed)/4294967296.0 - 0.5
	}
	shift := uint(32 - pi.bits)
	n := frames * int64(pi.channels)
	for i := int64(0); i < n; i++ {
		if _, err := io.ReadFull(r, sample); err != nil {
			return fail(err)
		}
		// to a left-aligned signed 32-bit value
		var v int32
		switch {
		case pi.bits == 8: // WAV 8-bit is unsigned; AIFF 8-bit is signed
			if pi.bigEnd {
				v = int32(int8(sample[0])) << 24
			} else {
				v = int32(int(sample[0])-128) << 24
			}
		case pi.bigEnd:
			var u uint32
			for _, c := range sample {
				u = u<<8 | uint32(c)
			}
			v = int32(u << shift)
		default:
			var u uint32
			for k := len(sample) - 1; k >= 0; k-- {
				u = u<<8 | uint32(sample[k])
			}
			v = int32(u << shift)
		}
		if outBits < pi.bits {
			step := float64(int64(1) << uint(32-outBits))
			x := float64(v) + (rnd()+rnd())*step
			x = math.Round(x/step) * step
			if x > math.MaxInt32 {
				x = float64(math.MaxInt32) - step + 1
			}
			if x < math.MinInt32 {
				x = math.MinInt32
			}
			v = int32(x)
		}
		u := uint32(v) >> uint(32-outBits)
		for k := 0; k < outBytes; k++ {
			if toAIFF {
				ob[outBytes-1-k] = byte(u >> (8 * k))
			} else {
				ob[k] = byte(u >> (8 * k))
			}
		}
		if _, err := w.Write(ob); err != nil {
			return fail(err)
		}
	}
	if dataLen%2 == 1 {
		w.WriteByte(0)
	}
	if err := w.Flush(); err != nil {
		return fail(err)
	}
	return out.Close()
}
