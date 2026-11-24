package signer

import "encoding/binary"

var oiConstants = [64]uint32{
	2813263392, 1900671446, 1206176263, 3920990055, 2478384350, 3323252981, 951050479, 2998644083,
	3796124867, 2637884571, 3749394404, 3635506496, 4172034488, 1991878923, 836252654, 2503986788,
	892704164, 1175044411, 915558408, 2355956005, 1576386935, 486274347, 2320476144, 290235230,
	2173958532, 2054850355, 829076693, 1008783305, 2799191123, 974671884, 2039399546, 2544331560,
	1724186518, 1366385731, 231930939, 3043585447, 3152755397, 1548997124, 3542485198, 1920580076,
	4095250127, 4158100443, 858571921, 2876681749, 2615928615, 413143940, 1542060199, 1603315540,
	968254400, 912735363, 2090005780, 2642253015, 3806910890, 1602133673, 2059224401, 1453196762,
	1084380722, 1039509925, 3132325248, 1161987804, 1063079544, 64468829, 2986232567, 2779457187,
}

var publicMd5ZiSeeds = [5]uint32{
	2560851803, 3874480130, 3239760911, 3714875250, 2728712785,
}

var publicMd5ZiPerms = [5][64]int{
	{8, 1, 12, 11, 9, 6, 0, 4, 13, 7, 3, 2, 10, 5, 14, 15, 8, 3, 6, 14, 11, 9, 15, 5, 0, 1, 10, 13, 7, 4, 2, 12, 13, 3, 11, 1, 14, 11, 4, 12, 6, 8, 5, 7, 2, 9, 15, 10, 13, 7, 15, 3, 12, 0, 6, 5, 5, 1, 4, 11, 2, 14, 9, 10},
	{9, 8, 13, 6, 10, 7, 14, 11, 0, 1, 2, 15, 12, 4, 5, 3, 9, 2, 7, 5, 6, 10, 3, 4, 14, 8, 12, 0, 1, 11, 15, 13, 0, 2, 6, 8, 5, 6, 11, 13, 7, 9, 4, 1, 15, 10, 3, 12, 0, 1, 3, 2, 13, 14, 7, 4, 4, 8, 11, 6, 15, 5, 10, 12},
	{10, 11, 7, 14, 9, 12, 2, 13, 3, 6, 15, 5, 0, 8, 1, 4, 10, 15, 12, 1, 14, 9, 4, 8, 2, 11, 0, 3, 6, 13, 5, 7, 3, 15, 14, 11, 1, 14, 13, 7, 12, 10, 8, 6, 5, 9, 4, 0, 3, 6, 4, 15, 7, 2, 12, 8, 8, 11, 13, 14, 5, 1, 9, 0},
	{7, 5, 11, 12, 6, 1, 15, 14, 13, 8, 4, 3, 9, 0, 10, 2, 7, 4, 1, 10, 12, 6, 2, 0, 15, 5, 9, 13, 8, 14, 3, 11, 13, 4, 12, 5, 10, 12, 14, 11, 1, 7, 0, 8, 3, 6, 2, 9, 13, 8, 2, 4, 11, 15, 1, 0, 0, 5, 14, 12, 3, 10, 6, 9},
	{5, 8, 3, 6, 4, 2, 0, 12, 10, 7, 15, 9, 13, 11, 1, 14, 5, 15, 2, 1, 6, 4, 14, 11, 0, 8, 13, 10, 7, 12, 9, 3, 10, 15, 6, 8, 1, 6, 12, 3, 2, 5, 11, 7, 9, 4, 14, 13, 10, 7, 14, 15, 3, 0, 2, 11, 11, 8, 12, 6, 9, 1, 4, 13},
}

func md5RoundOp(round int, e, t, n, i, o, a, c uint32) uint32 {
	var s uint32
	switch round {
	case 0:
		s = (t & n) | ((^t) & i)
	case 1:
		s = (t & i) | (n & (^i))
	case 2:
		s = t ^ n ^ i
	default:
		s = n ^ (t | (^i))
	}
	return t + rotl32(e+s+o+c, uint(a))
}

// publicMd5Mi implements PublicMd5Mi (REF L815), returning the 16-byte head.
func publicMd5Mi(block64 []byte, e uint32, perm [64]int, n uint32) []byte {
	var i [64]uint32
	for k := 0; k < 64; k++ {
		i[k] = rotr32(oiConstants[k], uint(n))
	}
	o := [4]uint32{
		rotr32(2044785403, uint(e)),
		rotr32(3367314800, uint(e)),
		rotr32(3955423437, uint(e)),
		rotr32(2081443219, uint(e)),
	}
	a := (e + 6) & 255
	c := [4]uint32{
		rotr32(431900774, uint(a)),
		rotr32(3898181300, uint(a)),
		rotr32(3785044590, uint(a)),
		rotr32(1909577684, uint(a)),
	}
	var s [16]uint32
	for k := 0; k < 16; k++ {
		s[k] = binary.LittleEndian.Uint32(block64[k*4 : k*4+4])
	}
	u, f, h, d := o[0], o[1], o[2], o[3]

	shifts := [4][16]uint32{
		{7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22, 7, 12, 17, 22},
		{5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20, 5, 9, 14, 20},
		{4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23, 4, 11, 16, 23},
		{6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21, 6, 10, 15, 21},
	}

	for round := 0; round < 4; round++ {
		p := round * 16
		l := shifts[round]
		for g := 0; g < 16; g++ {
			msg := s[perm[p+g]]
			k := i[p+g]
			switch g & 3 {
			case 0:
				u = md5RoundOp(round, u, f, h, d, msg, l[g], k)
			case 1:
				d = md5RoundOp(round, d, u, f, h, msg, l[g], k)
			case 2:
				h = md5RoundOp(round, h, d, u, f, msg, l[g], k)
			default:
				f = md5RoundOp(round, f, h, d, u, msg, l[g], k)
			}
		}
	}

	out := make([]byte, 16)
	binary.LittleEndian.PutUint32(out[0:4], (o[0]+u)^c[0])
	binary.LittleEndian.PutUint32(out[4:8], (o[1]+f)^c[1])
	binary.LittleEndian.PutUint32(out[8:12], (o[2]+h)^c[2])
	binary.LittleEndian.PutUint32(out[12:16], (o[3]+d)^c[3])
	return out
}

// publicMd5Zi implements PublicMd5Zi (REF L816), returning the 16-byte head.
func publicMd5Zi(selector uint32, qBytes, bodyBytes, epochLE []byte, epoch uint32) []byte {
	o := (selector & 13) * 86
	a := ((o >> 15) & 255) + ((o >> 8) & 255)
	c := publicMd5ZiSeeds[a]
	s := (c + epoch + 1) & 255
	u := c + epoch
	f := (u + 5) & 7

	h := [8]uint32{132, 150, 119, 157, 212, 21, 11, 248}
	var x [64]byte
	copy(x[0:32], qBytes)
	copy(x[32:48], bodyBytes)
	copy(x[48:52], epochLE)
	for idx, hv := range h {
		x[52+idx] = byte(((hv | (hv << 8)) >> f) & 255)
	}
	x[60], x[61], x[62], x[63] = 0xa0, 0x01, 0x00, 0x00

	return publicMd5Mi(x[:], u, publicMd5ZiPerms[a], s)
}
