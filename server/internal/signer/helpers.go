package signer

import (
	"encoding/binary"
	"math/bits"
	"strconv"
	"strings"
)

// sm3Sum implements ht = tn() (SM3 hash, REF L814).
func sm3Sum(data []byte) [32]byte {
	paddedLen := ((len(data) + 9 + 63) / 64) * 64
	n := make([]byte, paddedLen)
	copy(n, data)
	n[len(data)] = 0x80
	bitLen := uint64(len(data)) * 8
	binary.BigEndian.PutUint64(n[paddedLen-8:], bitLen)

	iv := [8]uint32{
		1937774191, 1226093241, 388252375, 3666478592,
		2842636476, 372324522, 3817729613, 2969243214,
	}
	var a [68]uint32
	rot := func(u uint32, f int) uint32 {
		return bits.RotateLeft32(u, f&31)
	}

	for u := 0; u < paddedLen; u += 64 {
		for g := 0; g < 16; g++ {
			a[g] = binary.BigEndian.Uint32(n[u+g*4 : u+g*4+4])
		}
		for g := 16; g < 68; g++ {
			v := a[g-16] ^ a[g-9] ^ rot(a[g-3], 15)
			a[g] = v ^ rot(v, 15) ^ rot(v, 23) ^ rot(a[g-13], 7) ^ a[g-6]
		}
		f, h, d, x, y, iV, F, l := iv[0], iv[1], iv[2], iv[3], iv[4], iv[5], iv[6], iv[7]
		for g := 0; g < 64; g++ {
			v := rot(f, 12)
			tConst := uint32(2043430169)
			if g >= 16 {
				tConst = 2055708042
			}
			U := rot(v+y+rot(tConst, g), 7)
			R := U ^ v
			var L, N uint32
			if g < 16 {
				L = f ^ h ^ d
				N = y ^ iV ^ F
			} else {
				L = (f & h) | (f & d) | (h & d)
				N = (y & iV) | ((^y) & F)
			}
			k := L + x + R + (a[g] ^ a[g+4])
			T := N + l + U + a[g]
			x = d
			d = rot(h, 9)
			h = f
			f = k
			l = F
			F = rot(iV, 19)
			iV = y
			y = T ^ rot(T, 9) ^ rot(T, 17)
		}
		iv[0] ^= f
		iv[1] ^= h
		iv[2] ^= d
		iv[3] ^= x
		iv[4] ^= y
		iv[5] ^= iV
		iv[6] ^= F
		iv[7] ^= l
	}

	var out [32]byte
	for u := 0; u < 8; u++ {
		binary.BigEndian.PutUint32(out[u*4:u*4+4], iv[u])
	}
	return out
}

func rotl32(x uint32, n uint) uint32 {
	return bits.RotateLeft32(x, int(n&31))
}

func rotr32(x uint32, n uint) uint32 {
	return bits.RotateLeft32(x, -int(n&31))
}

func rotr64(x uint64, n uint) uint64 {
	return bits.RotateLeft64(x, -int(n&63))
}

func le32Bytes(v uint32) []byte {
	var b [4]byte
	binary.LittleEndian.PutUint32(b[:], v)
	return b[:]
}

func be32Bytes(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

// ce implements Ce(r, e) from REF L91266.
func ce(acc uint32, data []byte) uint32 {
	t := acc
	for n, b := range data {
		v := uint32(b)
		if (n & 1) == 0 {
			t = (t >> 4) ^ t ^ (t << 6) ^ v
		} else {
			t = ^((t >> 7) ^ t ^ (v | (t << 12)))
		}
	}
	return t
}

// rawChecksum implements rawChecksum(data, seed, count) from REF L905.
func rawChecksum(data []byte, seed uint32, count int) uint32 {
	e := seed
	for i := 0; i < count; i++ {
		var n, v uint32
		if (i & 1) != 0 {
			n = (e >> 5) ^ e
			v = ^(uint32(data[i]) | (e << 11))
		} else {
			n = (e >> 3) ^ e
			v = uint32(data[i]) ^ (e << 7)
		}
		e = v ^ n
	}
	return e
}

// encodeWordVarint encodes an unsigned 64-bit integer as protobuf base-128 varint (REF L969).
func encodeWordVarint(v uint64) []byte {
	var out [10]byte
	i := 0
	for v > 127 {
		out[i] = byte(v&127) | 128
		v >>= 7
		i++
	}
	out[i] = byte(v)
	return append([]byte(nil), out[:i+1]...)
}

// readWordVarint reads a 64-bit protobuf varint (REF L899).
func readWordVarint(data []byte, offset *int, invalidCode, truncatedCode string) (uint64, error) {
	var high, low uint32
	for i := 0; i < 10; i++ {
		if *offset >= len(data) {
			return 0, errCode(truncatedCode, "unexpected end of varint")
		}
		b := data[*offset]
		*offset++
		bitVal := uint32(b & 127)
		shift := uint(i * 7)
		if i == 9 && bitVal > 1 {
			return 0, errCode(invalidCode, "varint overflow")
		}
		if shift < 32 {
			low |= bitVal << shift
			if shift > 25 {
				high |= bitVal >> (32 - shift)
			}
		} else {
			high |= bitVal << (shift - 32)
		}
		if b < 128 {
			return (uint64(high) << 32) | uint64(low), nil
		}
	}
	return 0, errCode(invalidCode, "varint too long")
}

// protoField holds a decoded protobuf field value (either varint or length-delimited bytes).
type protoField struct {
	wire  uint64
	intV  uint64
	bytes []byte
}

// decodeFields decodes top-level protobuf wire=0 and wire=2 fields (REF L920).
func decodeFields(data []byte) (map[uint64]protoField, error) {
	fields := make(map[uint64]protoField)
	off := 0
	for off < len(data) {
		tag, err := readWordVarint(data, &off, "V5_PROTO_VARINT", "V5_PROTO_TRUNCATED")
		if err != nil {
			return nil, err
		}
		fieldNum := tag >> 3
		wire := tag & 7
		switch wire {
		case 0:
			v, err := readWordVarint(data, &off, "V5_PROTO_VARINT", "V5_PROTO_TRUNCATED")
			if err != nil {
				return nil, err
			}
			fields[fieldNum] = protoField{wire: 0, intV: v}
		case 2:
			l, err := readWordVarint(data, &off, "V5_PROTO_VARINT", "V5_PROTO_TRUNCATED")
			if err != nil {
				return nil, err
			}
			if l > uint64(len(data)-off) {
				return nil, errCode("V5_PROTO_LENGTH", "length exceeds buffer")
			}
			n := int(l)
			buf := make([]byte, n)
			copy(buf, data[off:off+n])
			off += n
			fields[fieldNum] = protoField{wire: 2, bytes: buf}
		default:
			return nil, errCode("V5_PROTO_WIRE", "unsupported wire type")
		}
	}
	return fields, nil
}

// zigzag64 implements signed zigzag encoding matching profileInteger / Li (REF L971).
func zigzag64(v int64) uint64 {
	if v < 0 {
		// -v * 2 - 1 without overflow for min int64
		return uint64(-(v+1))*2 + 1
	}
	return uint64(v) * 2
}

func appendProfileInteger(dst []byte, field uint64, value int64) []byte {
	dst = append(dst, encodeWordVarint(field*8)...)
	return append(dst, encodeWordVarint(zigzag64(value))...)
}

func appendProfileBytes(dst []byte, field uint64, value []byte) []byte {
	dst = append(dst, encodeWordVarint(field*8+2)...)
	dst = append(dst, encodeWordVarint(uint64(len(value)))...)
	return append(dst, value...)
}

// encodeQueryParam matches JS Ni(): encodeURIComponent(s).replace(/[!'()]/g, ...)
// Unescaped set: A-Z a-z 0-9 - _ . ~ *
func encodeQueryComponentJS(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	const upperHex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		isUnreserved := (c >= 'A' && c <= 'Z') ||
			(c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' || c == '*'
		if isUnreserved {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(upperHex[c>>4])
			b.WriteByte(upperHex[c&15])
		}
	}
	return b.String()
}

// QueryKV represents an ordered query parameter pair.
type QueryKV struct {
	Key   string
	Value string
}

// EncodeQuery serializes ordered query pairs using JS Ni() rules.
func EncodeQuery(pairs []QueryKV) string {
	var b strings.Builder
	for i, kv := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(encodeQueryComponentJS(kv.Key))
		b.WriteByte('=')
		b.WriteString(encodeQueryComponentJS(kv.Value))
	}
	return b.String()
}

// FormatInt64 formats an int64 in base 10.
func formatInt64(v int64) string {
	return strconv.FormatInt(v, 10)
}
