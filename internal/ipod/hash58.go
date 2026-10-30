package ipod

// The "hash58" signature that the iPod classic and the 3rd and 4th
// generation iPod nano check before they accept an iTunesDB: an HMAC-SHA1
// of the database whose key is derived from the iPod's FireWire ID.
//
// Ported from libgpod's itdb_hash58.c, which carries this notice:
//
//   Copyright (C) 2007 Christophe Fergeau <teuf@gnome.org>
//   The code in this file is heavily based on the proof-of-concept code
//   written by wtbw
//
//   Redistribution and use in source and binary forms, with or without
//   modification, are permitted provided that the following conditions are
//   met:
//     1. Redistributions of source code must retain the above copyright
//        notice, this list of conditions and the following disclaimer.
//     2. Redistributions in binary form must reproduce the above copyright
//        notice, this list of conditions and the following disclaimer in
//        the documentation and/or other materials provided with the
//        distribution.
//     3. The name of the author may not be used to endorse or promote
//        products derived from this software without specific prior
//        written permission.
//
//   THIS SOFTWARE IS PROVIDED BY THE AUTHOR ``AS IS'' AND ANY EXPRESS OR
//   IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED
//   WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
//   DISCLAIMED. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT,
//   INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
//   (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
//   SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
//   HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
//   STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING
//   IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
//   POSSIBILITY OF SUCH DAMAGE.
//
// The two 256-byte tables are the AES S-box and its inverse, so they're
// computed here rather than listed.

import (
	"crypto/sha1"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
)

var sbox, invSbox [256]byte

func init() {
	// AES S-box: multiplicative inverse in GF(2^8), then the affine map
	p, q := byte(1), byte(1)
	for {
		// p *= 3
		p = p ^ (p << 1) ^ func() byte {
			if p&0x80 != 0 {
				return 0x1B
			}
			return 0
		}()
		// q /= 3
		q ^= q << 1
		q ^= q << 2
		q ^= q << 4
		if q&0x80 != 0 {
			q ^= 0x09
		}
		x := q ^ rotl8(q, 1) ^ rotl8(q, 2) ^ rotl8(q, 3) ^ rotl8(q, 4)
		sbox[p] = x ^ 0x63
		if p == 1 {
			break
		}
	}
	sbox[0] = 0x63
	for i := 0; i < 256; i++ {
		invSbox[sbox[i]] = byte(i)
	}
}

func rotl8(x byte, n uint) byte { return x<<n | x>>(8-n) }

var hash58Fixed = []byte{0x67, 0x23, 0xFE, 0x30, 0x45, 0x33, 0xF8, 0x90, 0x99,
	0x21, 0x07, 0xC1, 0xD0, 0x12, 0xB2, 0xA1, 0x07, 0x81}

func gcd(a, b int) int {
	for {
		a %= b
		if a == 0 {
			return b
		}
		b %= a
		if b == 0 {
			return a
		}
	}
}

func lcm(a, b int) int {
	if a == 0 || b == 0 {
		return 1
	}
	return a * b / gcd(a, b)
}

// parseFirewireID turns "0x000A27001234ABCD" into its bytes (padded to 20).
func parseFirewireID(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) < 16 || len(s) > 40 || len(s)%2 != 0 {
		return nil, errors.New("the iPod's FireWire ID isn't valid")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, errors.New("the iPod's FireWire ID isn't valid")
	}
	out := make([]byte, 20)
	copy(out, b)
	return out, nil
}

func hash58Key(fwid []byte) []byte {
	var y [16]byte
	for i := 0; i < 4; i++ {
		l := lcm(int(fwid[i*2]), int(fwid[i*2+1]))
		hi, lo := byte(l>>8), byte(l)
		y[i*4] = sbox[hi]
		y[i*4+1] = invSbox[hi]
		y[i*4+2] = sbox[lo]
		y[i*4+3] = invSbox[lo]
	}
	h := sha1.New()
	h.Write(hash58Fixed)
	h.Write(y[:])
	key := make([]byte, 64)
	copy(key, h.Sum(nil))
	return key
}

// signHash58 writes the hash58 signature into a serialised iTunesDB.
func signHash58(db []byte, firewireID string) error {
	fwid, err := parseFirewireID(firewireID)
	if err != nil {
		return err
	}
	if len(db) < 0x6C || string(db[:4]) != "mhbd" || binary.LittleEndian.Uint32(db[4:]) < 0x6C {
		return errors.New("the database is too small to sign")
	}
	var dbid [8]byte
	var unk32 [20]byte
	copy(dbid[:], db[0x18:0x20])
	copy(unk32[:], db[0x32:0x46])
	clear(db[0x18:0x20])
	clear(db[0x32:0x46])
	clear(db[0x58:0x6C])
	binary.LittleEndian.PutUint16(db[0x30:], 1) // hashing scheme: hash58

	key := hash58Key(fwid)
	for i := range key {
		key[i] ^= 0x36
	}
	inner := sha1.New()
	inner.Write(key)
	inner.Write(db)
	ih := inner.Sum(nil)
	for i := range key {
		key[i] ^= 0x36 ^ 0x5c
	}
	outer := sha1.New()
	outer.Write(key)
	outer.Write(ih)
	copy(db[0x58:0x6C], outer.Sum(nil))

	copy(db[0x18:0x20], dbid[:])
	copy(db[0x32:0x46], unk32[:])
	return nil
}
