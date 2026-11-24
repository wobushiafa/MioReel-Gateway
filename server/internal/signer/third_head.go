package signer

import "encoding/binary"

// thirdHead implements thirdHead(selector, data, epoch) from REF L906.
func thirdHead(selector int, data []byte, epoch uint32) ([]byte, error) {
	profile, ok := tables.ThirdProfiles[selector]
	if !ok {
		return nil, errCode("V5_SELECTOR_UNSUPPORTED", "missing third profile")
	}
	T, ok := tables.Tables[selector]
	if !ok {
		return nil, errCode("V5_SELECTOR_UNSUPPORTED", "missing third table")
	}

	cVal := profile.PublicScalar
	initH := rotr64(uint64(epoch), uint((uint64(epoch)+cVal-1)&63))
	var h [8]uint64
	for i := 0; i < 8; i++ {
		h[i] = initH
	}

	rcShift := uint((uint64(epoch) + cVal) & 63)
	rc := make([]uint64, len(tables.LiteralRows))
	for i, lr := range tables.LiteralRows {
		rc[i] = rotr64(lr, rcShift)
	}

	// padded = data(52B) || 0x80 || 43 zeros || 32 zeros -> 128 bytes with [126]=1, [127]=160
	padded := make([]byte, len(data)+1+43+32)
	copy(padded, data)
	padded[len(data)] = 128
	if len(padded) < 128 {
		return nil, errCode("V5_INPUT_SHAPE", "thirdHead padded length < 128")
	}
	padded[126] = 1
	padded[127] = 160

	transform := func(v [8]uint64) [8]uint64 {
		var out [8]uint64
		for i := 0; i < 8; i++ {
			var acc uint64
			for j := 0; j < 8; j++ {
				w := v[(i-j)&7]
				idx := byte(w >> uint(56-8*j))
				acc ^= T[j][idx]
			}
			out[i] = acc
		}
		return out
	}

	for index := 0; index < 2; index++ {
		var blk [8]uint64
		for i := 0; i < 8; i++ {
			off := index*64 + i*8
			blk[i] = binary.BigEndian.Uint64(padded[off : off+8])
		}

		var key, state [8]uint64
		if index == 0 {
			key = h
			for i := 0; i < 8; i++ {
				state[i] = key[i] ^ blk[profile.FirstBlockInputPermutation[i]]
			}
		} else {
			for i := 0; i < 8; i++ {
				key[i] = h[profile.PaddedBlockKeyHashPermutation[i]]
			}
			state = key
			state[profile.PaddedBlockLengthXorCanonicalIndex] ^= 416
		}

		for i := 1; i <= 10; i++ {
			key = transform(key)
			key[0] ^= rc[i]
			state = transform(state)
			for j := 0; j < 8; j++ {
				state[j] ^= key[j]
			}
		}

		for i := 0; i < 8; i++ {
			h[i] = h[i] ^ state[i] ^ blk[(i+1)&7]
		}
	}

	// packed = h.slice().reverse().flatMap(v => [8,24,40,48].map(s => wordByte(v, s)))
	var packed [32]byte
	shifts := [4]uint{8, 24, 40, 48}
	pIdx := 0
	for i := 7; i >= 0; i-- {
		v := h[i]
		for _, s := range shifts {
			packed[pIdx] = byte(v >> s)
			pIdx++
		}
	}

	out := make([]byte, 16)
	for i := 0; i < 16; i++ {
		out[i] = packed[i] ^ packed[i+16]
	}
	return out, nil
}
