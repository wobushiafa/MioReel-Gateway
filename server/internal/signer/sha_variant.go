package signer

import "encoding/binary"

var publicShaUSeeds = [6]uint32{
	3997655695, 4272070413, 3565944766, 3294847055, 2638911426, 3879464831,
}

var publicShaK = [64]uint32{
	3954593711, 128638146, 3476129727, 3759225087, 1829502207, 1513137086, 3021657656, 873991864,
	4063488879, 2980414668, 3435335111, 1204935505, 3579204574, 1890078625, 1464286226, 4151247496,
	738688778, 3658336976, 4223168364, 4257533696, 1753874957, 3866543782, 2362848139, 440175886,
	3437801579, 1792077067, 2689738519, 3698437251, 579197809, 3585400230, 1936688720, 1700802668,
	1635574438, 3827515774, 172024131, 1414414386, 2296380202, 1902910176, 1279687304, 95566771,
	3788552294, 3131301048, 2922607827, 3275233872, 386070363, 4268598120, 4038381384, 1309670328,
	1923797261, 650459709, 4072464630, 3025499521, 1243971549, 2352054946, 2869640070, 1029569300,
	1326462516, 3548074711, 1146027798, 3943314385, 1822977902, 2640807, 137919738, 1605596550,
}

var publicShaInitV = [8]uint32{
	2059030472, 1729520903, 1677982208, 873419055, 984304912, 3005047866, 2874125293, 2152413264,
}

var publicShaConfigs = [6][11]int{
	{100, 4, 0, 2, 5, 6, 1, 3, 0, 7, 7},
	{96, 0, 5, 7, 2, 1, 3, 6, 5, 4, 2},
	{94, 3, 7, 0, 1, 2, 5, 6, 5, 4, 3},
	{97, 2, 6, 1, 5, 3, 4, 7, 1, 0, 4},
	{96, 1, 3, 4, 0, 6, 2, 5, 0, 7, 7},
	{99, 1, 6, 0, 3, 5, 2, 7, 5, 4, 6},
}

// publicSha implements PublicSha (REF L817), returning the 16-byte head.
func publicSha(qBytes, bodyBytes, epochLE []byte, epoch, selector uint32) []byte {
	a := selector / 3
	u := publicShaUSeeds[a]
	f := (u + epoch + 1) & 255
	h := u + epoch

	var x [64]uint32
	for k := 0; k < 64; k++ {
		x[k] = rotr32(publicShaK[k], uint(f))
	}

	y := (h + 2) & 7
	iArr := [4]uint32{250, 69, 97, 215}

	var l [64]byte
	copy(l[0:32], qBytes)
	copy(l[32:48], bodyBytes)
	copy(l[48:52], epochLE)
	for idx, iv := range iArr {
		l[52+idx] = byte(((iv | (iv << 8)) >> y) & 255)
	}
	l[62] = 0x01
	l[63] = 0xa0

	var p [128]uint32
	for k := 0; k < 16; k++ {
		p[k] = binary.BigEndian.Uint32(l[k*4 : k*4+4])
	}
	g := p[0]
	for k := 0; k < 112; k++ {
		T := p[k+1]
		b := p[k+14]
		A := rotl32(T, 14) ^ rotl32(T, 25) ^ (T >> 3)
		D := rotl32(b, 13) ^ rotl32(b, 15) ^ (b >> 10)
		p[16+k] = g + p[k+9] + A + D
		g = T
	}

	var v [8]uint32
	for k := 0; k < 8; k++ {
		v[k] = rotr32(publicShaInitV[k], uint(h&31))
	}

	cfg := publicShaConfigs[a]
	R := v

	for k := 0; k < cfg[0]; k++ {
		T := u + uint32(k)
		b := p[T&127]
		A := ((R[cfg[3]] ^ R[cfg[4]]) & R[cfg[1]]) ^ R[cfg[3]]
		D := rotl32(R[cfg[1]], 26) ^ rotl32(R[cfg[1]], 21) ^ rotl32(R[cfg[1]], 7)
		w := b + A + D + x[T&63] + R[cfg[5]]
		H := rotl32(R[cfg[2]], 30) ^ rotl32(R[cfg[2]], 19) ^ rotl32(R[cfg[2]], 10)
		O := (R[cfg[2]] & R[cfg[6]]) | ((R[cfg[2]] | R[cfg[6]]) & R[cfg[7]])
		P := H + O
		q := R[cfg[9]]

		// R.unshift(R.pop())
		last := R[7]
		copy(R[1:], R[:7])
		R[0] = last

		R[cfg[10]] = P + w
		R[cfg[8]] = q + w
	}

	var L [32]byte
	for k := 0; k < 8; k++ {
		binary.BigEndian.PutUint32(L[k*4:k*4+4], R[k]+v[k])
	}

	out := make([]byte, 16)
	for k := 0; k < 16; k++ {
		out[k] = L[k] ^ L[16+k]
	}
	return out
}
