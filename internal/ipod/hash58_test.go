package ipod

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSboxes(t *testing.T) {
	// first and last entries of libgpod's table1 (the AES S-box) and table2
	if sbox[0] != 0x63 || sbox[1] != 0x7C || sbox[0x53] != 0xED || sbox[255] != 0x16 {
		t.Fatalf("sbox: %x %x %x %x", sbox[0], sbox[1], sbox[0x53], sbox[255])
	}
	if invSbox[0] != 0x52 || invSbox[1] != 0x09 || invSbox[255] != 0x7D {
		t.Fatalf("inverse sbox: %x %x %x", invSbox[0], invSbox[1], invSbox[255])
	}
}

// LIBGPOD_HASH58_DIR holds iPods whose iTunesDB libgpod wrote with the
// FireWire ID 0x000A2700138A4A0D; signing the same bytes must give the same
// signature.
func TestHash58MatchesLibgpod(t *testing.T) {
	dir := os.Getenv("LIBGPOD_HASH58_DIR")
	if dir == "" {
		t.Skip("LIBGPOD_HASH58_DIR not set")
	}
	n := 0
	for _, model := range []string{"xB029", "xB565", "xA978"} {
		b, err := os.ReadFile(filepath.Join(dir, model, "iPod_Control", "iTunes", "iTunesDB"))
		if err != nil {
			t.Fatal(err)
		}
		want := append([]byte(nil), b[0x58:0x6C]...)
		mine := append([]byte(nil), b...)
		if err := signHash58(mine, "0x000A2700138A4A0D"); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mine, b) {
			t.Fatalf("%s: signature %x, libgpod %x", model, mine[0x58:0x6C], want)
		}
		n++
	}
	if n != 3 {
		t.Fatal("not all checked")
	}
}
