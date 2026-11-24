package signer

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// publicProfileHelios implements PublicProfileHelios (REF L967).
func publicProfileHelios(epoch uint32, publicID []byte, rand32 uint32) string {
	t := make([]byte, 8)
	binary.LittleEndian.PutUint32(t[0:4], rand32)
	copy(t[4:8], "8662")

	sum := md5.Sum(t)
	hexAscii := []byte(hex.EncodeToString(sum[:]))

	var o [4]uint64
	for d := 0; d < 4; d++ {
		o[d] = binary.LittleEndian.Uint64(hexAscii[d*8 : d*8+8])
	}

	a := make([]uint64, 0, 35)
	a = append(a, o[0])
	c := o[0]
	s := o[1]
	queue := []uint64{o[2], o[3]}

	for d := 0; d < 34; d++ {
		x := rotr64(s, 8) + c
		x ^= uint64(d)
		queue = append(queue, x)
		x ^= rotr64(c, 61)
		a = append(a, x)
		c = x
		s = queue[0]
		queue = queue[1:]
	}

	plain := fmt.Sprintf("%d-%s-8662", epoch, string(publicID))
	padLen := 16 - (len(plain) % 16)
	padded := make([]byte, len(plain)+padLen)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	out := make([]byte, 4+len(padded))
	binary.LittleEndian.PutUint32(out[0:4], rand32)

	for d := 0; d < len(padded); d += 16 {
		x := binary.LittleEndian.Uint64(padded[d : d+8])
		y := binary.LittleEndian.Uint64(padded[d+8 : d+16])
		for f := 0; f < 34; f++ {
			y = a[f] ^ (x + rotr64(y, 8))
			x = y ^ rotr64(x, 61)
		}
		binary.LittleEndian.PutUint64(out[4+d:4+d+8], x)
		binary.LittleEndian.PutUint64(out[4+d+8:4+d+16], y)
	}

	return base64.StdEncoding.EncodeToString(out)
}
