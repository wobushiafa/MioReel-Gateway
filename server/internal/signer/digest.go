package signer

import (
	"encoding/binary"
)

type componentsInput struct {
	QueryBytes       []byte
	QueryHash        []byte // optional 32 bytes; computed as SHA256(QueryBytes) if nil
	BodyHash         []byte // 16 bytes MD5(body) or 16 zero bytes
	Epoch            uint32
	NormalJNI        bool
	Material         []byte // optional 32 or 64 bytes
	Nonce            uint32
	Proto            []byte // optional
	SelectorOverride *int
}

type componentsOutput struct {
	QueryHash   []byte // 32 bytes
	Selector    int    // 0..15
	MixedDigest []byte // 20 bytes
	PrefixFlag  byte
	StreamSeed  []byte // 32 bytes
	StreamKey   []byte // 4 bytes
	Xi          []byte // len(Proto) bytes
}

func computeSelector(queryHash, bodyHash []byte, epoch uint32) int {
	acc := ce(539167016, queryHash)
	acc = ce(acc, bodyHash)
	acc = ce(acc, le32Bytes(epoch))
	return int(acc & 15)
}

func components(in componentsInput) (componentsOutput, error) {
	qHash := in.QueryHash
	if len(qHash) == 0 {
		h := sm3Sum(in.QueryBytes)
		qHash = h[:]
	}

	selector := computeSelector(qHash, in.BodyHash, in.Epoch)
	if in.SelectorOverride != nil {
		selector = *in.SelectorOverride & 15
	}

	epochLE := le32Bytes(in.Epoch)
	var head []byte
	var err error
	switch selector % 3 {
	case 2:
		head = publicMd5Zi(uint32(selector), qHash, in.BodyHash, epochLE, in.Epoch)
	case 0:
		head = publicSha(qHash, in.BodyHash, epochLE, in.Epoch, uint32(selector))
	default:
		buf := make([]byte, 0, len(qHash)+len(in.BodyHash)+4)
		buf = append(buf, qHash...)
		buf = append(buf, in.BodyHash...)
		buf = append(buf, epochLE...)
		head, err = thirdHead(selector, buf, in.Epoch)
		if err != nil {
			return componentsOutput{}, err
		}
	}

	count := 8
	if in.NormalJNI {
		count = 12
	}
	sum := rawChecksum(head, 539100192, count)
	if in.NormalJNI {
		sum = (sum | 4 | 32) ^ 16777216
	} else {
		sum = (sum & ^uint32(4) & ^uint32(32)) ^ 16777216
	}

	digest := make([]byte, 20)
	copy(digest[0:16], head)
	binary.LittleEndian.PutUint32(digest[16:20], sum)

	out := componentsOutput{
		QueryHash:   qHash,
		Selector:    selector,
		MixedDigest: digest,
	}

	if len(in.Material) > 0 {
		seed, streamKey := deriveZi(in.Material, in.Nonce)
		out.PrefixFlag = byte(rawChecksum(in.Material, 0, len(in.Material)) & 255)
		out.StreamSeed = seed
		out.StreamKey = streamKey
		if len(in.Proto) > 0 {
			out.Xi = computeXi(in.Proto, seed)
		}
	}

	return out, nil
}

// deriveZi implements Zi(r, e) from REF L814.
func deriveZi(material []byte, nonce uint32) (seed []byte, streamKey []byte) {
	buf := make([]byte, 0, len(material)*2+4)
	buf = append(buf, material...)
	buf = append(buf, le32Bytes(nonce)...)
	buf = append(buf, material...)
	h := sm3Sum(buf)

	i := (nonce >> 16) & 255
	o := ((i << 11) | (nonce >> 24)) ^ (i >> 5) ^ i
	o = ^o
	return h[:], le32Bytes(o)
}

// computeXi implements Xi(r, e) from REF L814.
func computeXi(proto, seed []byte) []byte {
	nLen := len(proto)
	t := make([]byte, nLen)
	for a := 0; a < nLen; a++ {
		c := (a * 4) & 28
		s := seed[c]
		u := seed[c+1]
		f := byte(((proto[a] << 4) | (proto[a] >> 4)) + s)
		f = (^f) ^ u
		f = (f << 3) | (f >> 5)
		f = f + u
		f = f ^ s
		t[nLen-a-1] = ^f
	}
	if nLen < 4 {
		return t
	}
	n := int32(t[nLen-1] ^ t[nLen-2])
	i := int32(t[0])
	t[0] = byte((^n) + i)
	t[1] = byte((int32(t[0]^t[nLen-1]^254) + int32(t[1])) & 255)
	// JS: t[2] = t[2] + (n - i ^ (t[1]<<3|t[1]>>>5)&255 ^ 2) & 255
	rotT1 := int32((t[1] << 3) | (t[1] >> 5))
	t[2] = byte((int32(t[2]) + ((n - i) ^ rotT1 ^ 2)) & 255)
	for a := 0; a < nLen-4; a++ {
		rot := int32((t[a+2] << 3) | (t[a+2] >> 5))
		c := rot ^ int32(t[a+1]) ^ int32(a+3)
		t[a+3] = byte((^c) + int32(t[a+3]))
	}
	t[nLen-1] ^= t[nLen-2]
	var o int
	for a := 0; a < nLen-1; a++ {
		o += int(t[a+1])
	}
	t[0] = byte((int(t[0]^t[1]) + o) & 255)
	return t
}
