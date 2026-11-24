package signer

import (
	"bytes"
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
)

func extractPlanes(data []byte) []byte {
	out := make([]byte, 0, 31)
	words := len(data) / 8
	for _, plane := range tables.Planes {
		for w := 0; w < words; w++ {
			var val byte
			for d := 0; d < 8; d++ {
				s := plane.OutputToSource[d]
				bit := (data[w*8+s/8] >> uint(s%8)) & 1
				val |= bit << uint(d)
			}
			out = append(out, val)
			if len(out) == 31 {
				return out
			}
		}
	}
	return out
}

func embedPlanes(data, coded []byte) []byte {
	out := append([]byte(nil), data...)
	words := len(data) / 8
	idx := 0
	for _, plane := range tables.Planes {
		for w := 0; w < words; w++ {
			for d := 0; d < 8; d++ {
				s := plane.OutputToSource[d]
				at := w*8 + s/8
				m := byte(1 << uint(s%8))
				bit := (coded[idx] >> uint(d)) & 1
				out[at] = (out[at] & ^m) | (bit * m)
			}
			idx++
			if idx == 31 {
				return out
			}
		}
	}
	return out
}

func expandKey(key []byte, box [256]byte, scalar uint32) [48]byte {
	var out [48]byte
	for i := 0; i < 16; i++ {
		out[i] = key[i] ^ byte(scalar>>uint((i%4)*8))
	}
	counter := uint(8)
	for word := 4; word < 12; word++ {
		prev := 4 * (word - 1)
		a, b, c, d := out[prev], out[prev+1], out[prev+2], out[prev+3]
		if word%4 == 0 {
			a, b, c, d = byte(scalar>>(counter&24))^box[b], box[c], box[d], box[a]
		}
		counter += 2
		out[prev+4] = a ^ out[prev-12]
		out[prev+5] = b ^ out[prev-11]
		out[prev+6] = c ^ out[prev-10]
		out[prev+7] = d ^ out[prev-9]
	}
	return out
}

func xtime(v byte) byte {
	res := v << 1
	if v&128 != 0 {
		res ^= 27
	}
	return res
}

func blockEncrypt(data []byte, key []byte, prof CipherProfile) [16]byte {
	keys := expandKey(key, prof.Box, prof.KeyExpansionScalar)
	var state [16]byte
	copy(state[:], data[:16])

	add := func(start int) {
		for r := 0; r < 4; r++ {
			for c := 0; c < 4; c++ {
				state[r*4+c] ^= keys[(start+r)*4+prof.Perm[c]]
			}
		}
	}

	add(0)
	for round := 1; round <= 2; round++ {
		var shifted [16]byte
		for i := 0; i < 16; i++ {
			shifted[i] = prof.Box[state[prof.Shift[i]]]
		}
		state = shifted

		if round == 1 {
			var mid [16]byte
			for r := 0; r < 4; r++ {
				for c := 0; c < 4; c++ {
					mid[r*4+c] = state[r*4+prof.Middle[c]]
				}
			}
			state = mid

			for c := 0; c < 4; c++ {
				total := state[c] ^ state[4+c] ^ state[8+c] ^ state[12+c]
				first := state[c]
				for r := 0; r < 3; r++ {
					state[r*4+c] ^= total ^ xtime(state[r*4+c]^state[(r+1)*4+c])
				}
				state[12+c] ^= total ^ xtime(state[12+c]^first)
			}
		}
		add(round * 4)
	}

	for r := 0; r < 4; r++ {
		for c := 0; c < 4; c++ {
			state[r*4+c] ^= keys[(4+r)*4+c]
		}
	}
	return state
}

func cipherCBC(data, key, iv []byte, prof CipherProfile) []byte {
	padding := 16 - (len(data) % 16)
	input := make([]byte, len(data)+padding)
	copy(input, data)
	for i := len(data); i < len(input); i++ {
		input[i] = byte(padding)
	}

	result := make([]byte, len(input))
	var prev [16]byte
	copy(prev[:], iv[:16])
	for i := 0; i < len(input); i += 16 {
		var blk [16]byte
		for j := 0; j < 16; j++ {
			blk[j] = input[i+j] ^ prev[j]
		}
		prev = blockEncrypt(blk[:], key, prof)
		copy(result[i:i+16], prev[:])
	}
	return result
}

type packetInput struct {
	Proto    []byte
	Material []byte
	Randoms  [3]uint32
	Query    []byte
	BodyHash []byte
	Epoch    uint32
}

// buildPacket implements packet(input) from REF L943 and returns base64-encoded x-medusa.
func buildPacket(in packetInput) (string, error) {
	if len(in.Material) != 32 && len(in.Material) != 64 {
		return "", errCode("V5_INPUT_SHAPE", "invalid material length")
	}
	fields, err := decodeFields(in.Proto)
	if err != nil {
		return "", err
	}

	f2, ok2 := fields[2]
	f3, ok3 := fields[3]
	f12, ok12 := fields[12]
	if !ok2 || !ok3 || !ok12 || f2.wire != 0 || f3.wire != 0 || f12.wire != 0 {
		return "", errCode("V5_INPUT_BINDING", "missing binding varints")
	}
	if f2.intV != 10 || f3.intV != uint64(in.Randoms[0])*2 || f12.intV != uint64(in.Epoch)*2 {
		return "", errCode("V5_INPUT_BINDING", "input binding mismatch")
	}

	f13, ok13 := fields[13]
	f14, ok14 := fields[14]
	if !ok13 || !ok14 || f13.wire != 2 || f14.wire != 2 {
		return "", errCode("V5_DIGEST_BINDING", "missing digest fields")
	}

	digest, err := components(componentsInput{
		QueryBytes: in.Query,
		BodyHash:   in.BodyHash,
		Epoch:      in.Epoch,
		NormalJNI:  false,
	})
	if err != nil {
		return "", err
	}
	normalJni := false
	if !bytes.Equal(digest.MixedDigest, f13.bytes) {
		normalJni = true
		digest, err = components(componentsInput{
			QueryBytes: in.Query,
			BodyHash:   in.BodyHash,
			Epoch:      in.Epoch,
			NormalJNI:  true,
		})
		if err != nil {
			return "", err
		}
	}
	if !bytes.Equal(digest.MixedDigest, f13.bytes) || len(f14.bytes) != 6 || !bytes.Equal(digest.QueryHash[:6], f14.bytes) {
		return "", errCode("V5_DIGEST_BINDING", "digest binding mismatch")
	}

	nonce := in.Randoms[1]
	stream, err := components(componentsInput{
		QueryBytes: in.Query,
		BodyHash:   in.BodyHash,
		Epoch:      in.Epoch,
		NormalJNI:  normalJni,
		Proto:      in.Proto,
		Material:   in.Material,
		Nonce:      nonce,
	})
	if err != nil {
		return "", err
	}

	prefix := fields[10].bytes
	seed := stream.StreamKey
	body := make([]byte, len(prefix)+len(stream.Xi))
	copy(body, prefix)
	copy(body[len(prefix):], stream.Xi)
	for l, r := 0, len(body)-1; l < r; l, r = l+1, r-1 {
		body[l], body[r] = body[r], body[l]
	}
	for i := range body {
		body[i] ^= seed[byte(^i)&3]
	}

	flags := ((uint32(digest.QueryHash[0]) & 63) << 14) | 402653185 | ((uint32(digest.MixedDigest[0]) & 63) << 8)
	var head [9]byte
	head[0] = stream.PrefixFlag
	binary.LittleEndian.PutUint32(head[1:5], in.Randoms[2])
	binary.LittleEndian.PutUint32(head[5:9], flags)

	tail := le32Bytes(nonce)

	data := make([]byte, 0, 9+len(body)+2)
	data = append(data, head[:]...)
	data = append(data, body...)
	data = append(data, tail[2:4]...)

	extracted := extractPlanes(data)
	if len(extracted) != 31 {
		return "", errCode("V5_EXTRACTION_SIZE", "extracted size != 31")
	}

	keyHash := md5.Sum(in.Material[0:16])
	ivHash := md5.Sum(in.Material[16:32])
	coded := cipherCBC(extracted, keyHash[:], ivHash[:], tables.CipherProfiles[in.Epoch&3])

	f1 := fields[1].bytes
	if len(f1) != 16 {
		return "", errCode("V5_INPUT_SHAPE", "field 1 magic != 16 bytes")
	}
	var header [20]byte
	header[0] = 5
	copy(header[4:20], f1)
	for i := 0; i < 20; i += 4 {
		v := binary.LittleEndian.Uint32(header[i : i+4])
		binary.LittleEndian.PutUint32(header[i:i+4], v^in.Epoch)
	}

	var meta [4]byte
	binary.LittleEndian.PutUint16(meta[0:2], uint16(nonce&65535))
	meta[3] = byte(len(coded) - 31)

	embedded := embedPlanes(data, coded)
	out := make([]byte, 0, 20+4+len(coded)-31+len(embedded))
	out = append(out, header[:]...)
	out = append(out, meta[:]...)
	out = append(out, coded[31:]...)
	out = append(out, embedded...)
	return base64.StdEncoding.EncodeToString(out), nil
}

func hexString(b []byte) string {
	return hex.EncodeToString(b)
}
