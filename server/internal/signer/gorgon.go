package signer

import (
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
)

func viBitRev(r byte) byte {
	var e byte
	for t := 0; t < 8; t++ {
		e = (e << 1) | ((r >> uint(t)) & 1)
	}
	return e
}

// weirdRC4 implements $i(r, e) from REF L814 (note: non-swapping KSA and PRGA).
func weirdRC4(data []byte, key []byte) []byte {
	var t [256]byte
	for o := 0; o < 256; o++ {
		t[o] = byte(o)
	}
	n := 0
	for o := 0; o < 256; o++ {
		n = (n + int(t[o]) + int(key[o%len(key)])) & 255
		t[o] = t[n]
	}
	var i byte
	n = 0
	out := make([]byte, len(data))
	for idx, o := range data {
		i++
		a := t[i]
		n += int(a)
		c := t[n&255]
		t[i] = c
		out[idx] = o ^ t[(int(c)+int(c))&255]
	}
	return out
}

// publicProfileGorgon implements PublicProfileGorgon (REF L968).
func publicProfileGorgon(query string, body []byte, epoch uint32, rand16 uint16) string {
	var o [20]byte
	qSum := md5.Sum([]byte(query))
	copy(o[0:4], qSum[0:4])
	if len(body) > 0 {
		bSum := md5.Sum(body)
		copy(o[4:8], bSum[0:4])
	}
	// o[8:12] = 0
	binary.LittleEndian.PutUint32(o[12:16], 67764992)
	binary.BigEndian.PutUint32(o[16:20], epoch)

	a := [8]byte{74, 64, 22, byte(rand16 >> 8), 71, 108, 1, byte(rand16)}
	c := weirdRC4(o[:], a[:])

	for s := 0; s < len(c); s++ {
		u := (c[s] >> 4) | (c[s] << 4)
		var f byte
		if s+1 < len(c) {
			f = c[s+1]
		} else {
			f = c[0]
		}
		c[s] = ^(viBitRev(f^u) ^ 20)
	}

	out := make([]byte, 6+len(c))
	out[0], out[1] = 0x84, 0x04
	out[2], out[3], out[4], out[5] = byte(rand16), byte(rand16>>8), 64, 1
	copy(out[6:], c)
	return hex.EncodeToString(out)
}
