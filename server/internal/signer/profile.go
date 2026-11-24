package signer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"regexp"
	"strings"
	"sync/atomic"
)

const (
	SDKVersion           = "v04.10.03-ml-android"
	BuiltInProfileIDB64  = "NjQ2NjM3Nzk0"
	BuiltInProfileMatB64 = "sjwqRwog0QXcP4xEOPgnLtRtbIn/3N7dUQhpjSbpSJ0="
)

var (
	protoMagicField1, _ = hex.DecodeString("2d4b4fca49750d433fb5ae2c226dcc56")
	deviceIDPattern     = regexp.MustCompile(`^[1-9][0-9]{9,23}$`)
)

// Profile signs upstream Hongguo requests with the 6 required headers.
type Profile struct {
	id         []byte
	material   []byte
	sdkVersion string
	rand       RandSource
	sequence   atomic.Int64
}

// BuiltInConfiguration returns the embedded fallback configuration (REF L1000).
func BuiltInConfiguration() Configuration {
	id, _ := base64.StdEncoding.DecodeString(BuiltInProfileIDB64)
	mat, _ := base64.StdEncoding.DecodeString(BuiltInProfileMatB64)
	return Configuration{ID: id, Material: mat}
}

// NewSigningProfile validates cfg & sdkVersion and constructs a Profile.
func NewSigningProfile(cfg Configuration, sdkVersion string, randSrc RandSource) (*Profile, error) {
	if sdkVersion != SDKVersion {
		return nil, errCode("LOCAL_PROFILE_UNSUPPORTED", "unsupported sdkVersion: "+sdkVersion)
	}
	if len(cfg.ID) < 1 || len(cfg.ID) > 128 || len(cfg.Material) != 32 {
		return nil, errCode("SIGNING_CONFIGURATION_INVALID", "invalid id or material length")
	}
	if randSrc == nil {
		randSrc = CryptoRand{}
	}
	idCopy := append([]byte(nil), cfg.ID...)
	matCopy := append([]byte(nil), cfg.Material...)
	return &Profile{
		id:         idCopy,
		material:   matCopy,
		sdkVersion: sdkVersion,
		rand:       randSrc,
	}, nil
}

// BuiltInProfile returns a Profile initialized with the built-in fallback material.
func BuiltInProfile(randSrc RandSource) *Profile {
	p, err := NewSigningProfile(BuiltInConfiguration(), SDKVersion, randSrc)
	if err != nil {
		panic(err)
	}
	return p
}

// ID returns a copy of the profile ID bytes (ASCII digits).
func (p *Profile) ID() string {
	return string(p.id)
}

// SDKVersion returns the configured sdkVersion string.
func (p *Profile) SDKVersion() string {
	return p.sdkVersion
}

// Produce builds the protobuf payload and x-medusa packet (REF L989).
func (p *Profile) Produce(in ProduceInput) (ProduceResult, error) {
	qIdx := strings.IndexByte(in.URL, '?')
	var query string
	if qIdx >= 0 {
		query = in.URL[qIdx+1:]
	} else {
		query = in.URL
	}

	var bodyHash [16]byte
	if in.Body != nil {
		bodyHash = md5.Sum(in.Body)
	}

	var counter int64
	if in.Counter != nil {
		counter = int64(*in.Counter)
	} else {
		counter = p.sequence.Add(1)
	}
	if counter < 1 || counter > 2147483647 {
		return ProduceResult{}, errCode("LOCAL_PROFILE_STATE", "counter out of range")
	}

	if !deviceIDPattern.MatchString(in.Device.DeviceID) {
		return ProduceResult{}, errCode("LOCAL_PROFILE_DEVICE", "invalid device_id")
	}

	digest, err := components(componentsInput{
		QueryBytes: []byte(query),
		BodyHash:   bodyHash[:],
		Epoch:      in.Epoch,
	})
	if err != nil {
		return ProduceResult{}, err
	}

	state := make([]byte, 0, 32)
	state = appendProfileInteger(state, 1, counter)
	state = appendProfileInteger(state, 2, 694367)
	state = appendProfileInteger(state, 3, 694367)
	state = appendProfileInteger(state, 5, 694367)

	proto := make([]byte, 0, 256)
	proto = appendProfileBytes(proto, 1, protoMagicField1)
	proto = appendProfileInteger(proto, 2, 5)
	proto = appendProfileInteger(proto, 3, int64(in.Randoms[0]))
	proto = appendProfileBytes(proto, 4, []byte("8662"))
	proto = appendProfileBytes(proto, 5, []byte(in.Device.DeviceID))
	proto = appendProfileBytes(proto, 6, p.id)
	proto = appendProfileBytes(proto, 8, []byte(p.sdkVersion))
	proto = appendProfileInteger(proto, 9, 67764992)
	proto = appendProfileBytes(proto, 10, make([]byte, 8))
	proto = appendProfileInteger(proto, 12, int64(in.Epoch))
	proto = appendProfileBytes(proto, 13, digest.MixedDigest)
	proto = appendProfileBytes(proto, 14, digest.QueryHash[:6])
	proto = appendProfileBytes(proto, 15, state)
	proto = appendProfileInteger(proto, 17, int64(in.Epoch))

	sig, err := buildPacket(packetInput{
		Proto:    proto,
		Material: p.material,
		Randoms:  in.Randoms,
		Query:    []byte(query),
		BodyHash: bodyHash[:],
		Epoch:    in.Epoch,
	})
	if err != nil {
		return ProduceResult{}, err
	}

	return ProduceResult{
		Proto:     proto,
		Body:      in.Body,
		Query:     query,
		Signature: sig,
	}, nil
}

// Sign generates all 6 signature headers in the exact RNG consumption order of REF L997.
func (p *Profile) Sign(in SignInput) (SignHeaders, error) {
	var randoms [3]uint32
	for i := 0; i < 3; i++ {
		r, err := p.rand.Uint32()
		if err != nil {
			return SignHeaders{}, err
		}
		randoms[i] = r & 0x7fffffff
	}

	prod, err := p.Produce(ProduceInput{
		URL:     in.URL,
		Body:    in.Body,
		Epoch:   in.Epoch,
		Device:  in.Device,
		Randoms: randoms,
		Counter: in.Counter,
	})
	if err != nil {
		return SignHeaders{}, err
	}

	gorgonRand, err := p.rand.Uint16()
	if err != nil {
		return SignHeaders{}, err
	}
	heliosRand, err := p.rand.Uint32Big()
	if err != nil {
		return SignHeaders{}, err
	}

	return SignHeaders{
		XGorgon:  publicProfileGorgon(prod.Query, prod.Body, in.Epoch, gorgonRand),
		XKhronos: formatInt64(int64(in.Epoch)),
		XLadon:   base64.StdEncoding.EncodeToString(be32Bytes(in.Epoch)),
		XArgus:   base64.StdEncoding.EncodeToString(le32Bytes(in.Epoch)),
		XHelios:  publicProfileHelios(in.Epoch, p.id, heliosRand),
		XMedusa:  prod.Signature,
	}, nil
}

// DecodeLocalProfile decodes an APK-extracted local signing profile (REF L867, L973).
func DecodeLocalProfile(encoded string) (Configuration, error) {
	if len(encoded) > 131072 || len(encoded)%4 != 0 {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid encoded length")
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid base64")
	}
	if len(data) < 20 || (len(data)-4)%16 != 0 {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid outer size")
	}

	app := []byte("8662")
	prefix := data[:4]
	keyInput := make([]byte, 0, len(app)*2+len(prefix))
	keyInput = append(keyInput, app...)
	keyInput = append(keyInput, prefix...)
	keyInput = append(keyInput, app...)
	keySum := md5.Sum(keyInput)

	block, err := aes.NewCipher(keySum[:])
	if err != nil {
		return Configuration{}, err
	}
	padded := make([]byte, len(data)-4)
	iv := make([]byte, 16)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(padded, data[4:])

	pad := int(padded[len(padded)-1])
	if pad < 1 || pad > 16 || pad > len(padded) {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid padding")
	}
	for _, b := range padded[len(padded)-pad:] {
		if int(b) != pad {
			return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid padding bytes")
		}
	}
	plain := padded[:len(padded)-pad]
	if len(plain) < 9 {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "decrypted payload too short")
	}

	head := plain[:4]
	tail := plain[len(plain)-4:]
	shaInput := make([]byte, 0, len(head)+len(app)+len(prefix))
	shaInput = append(shaInput, head...)
	shaInput = append(shaInput, app...)
	shaInput = append(shaInput, prefix...)
	rc4Key := sha1.Sum(shaInput)

	inner := standardRC4(plain[4:len(plain)-4], rc4Key[:])
	var mask byte
	for _, b := range prefix {
		mask ^= b
	}
	for _, b := range head {
		mask ^= b
	}
	for _, b := range tail {
		mask ^= b
	}
	decoded := make([]byte, len(inner))
	for i := 0; i < len(inner); i++ {
		decoded[i] = inner[len(inner)-1-i] ^ mask
	}

	fields, err := decodeFields(decoded)
	if err != nil {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", err.Error())
	}
	if string(fields[2].bytes) != "mssdk" || string(fields[5].bytes) != "8662" {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "unexpected app fields")
	}
	id := fields[3].bytes
	if len(id) < 1 || len(id) > 128 {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid id length")
	}

	// Scan repeated field 8 entries for sign_key
	var material []byte
	off := 0
	for off < len(decoded) {
		tag, err := readWordVarint(decoded, &off, "LOCAL_PROFILE_INVALID", "LOCAL_PROFILE_INVALID")
		if err != nil {
			return Configuration{}, err
		}
		field := tag >> 3
		wire := tag & 7
		if wire == 0 {
			if _, err := readWordVarint(decoded, &off, "LOCAL_PROFILE_INVALID", "LOCAL_PROFILE_INVALID"); err != nil {
				return Configuration{}, err
			}
			continue
		}
		if wire != 2 {
			return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "unexpected wire type")
		}
		length, err := readWordVarint(decoded, &off, "LOCAL_PROFILE_INVALID", "LOCAL_PROFILE_INVALID")
		if err != nil {
			return Configuration{}, err
		}
		if length > uint64(len(decoded)-off) {
			return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "length overflow")
		}
		val := decoded[off : off+int(length)]
		off += int(length)
		if field == 8 {
			item, err := decodeFields(val)
			if err != nil {
				return Configuration{}, errCode("LOCAL_PROFILE_INVALID", err.Error())
			}
			if string(item[1].bytes) == "sign_key" {
				if material != nil {
					return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "duplicate sign_key")
				}
				mat, err := base64.StdEncoding.DecodeString(string(item[2].bytes))
				if err != nil {
					return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "invalid sign_key base64")
				}
				material = mat
			}
		}
	}
	if len(material) != 32 {
		return Configuration{}, errCode("LOCAL_PROFILE_INVALID", "material length != 32")
	}
	return Configuration{ID: id, Material: material}, nil
}

func standardRC4(data, key []byte) []byte {
	var state [256]byte
	for i := 0; i < 256; i++ {
		state[i] = byte(i)
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j + int(state[i]) + int(key[i%len(key)])) & 255
		state[i], state[j] = state[j], state[i]
	}
	i, j := 0, 0
	out := make([]byte, len(data))
	for idx, b := range data {
		i = (i + 1) & 255
		j = (j + int(state[i])) & 255
		state[i], state[j] = state[j], state[i]
		out[idx] = b ^ state[(int(state[i])+int(state[j]))&255]
	}
	return out
}
