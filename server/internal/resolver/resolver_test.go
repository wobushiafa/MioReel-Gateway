package resolver

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/bits"
	"testing"
)

func encodeTestSpadeA(keyHex string) string {
	payload := make([]byte, 39)
	for i := range payload {
		payload[i] = 'A'
	}
	copy(payload[1:33], keyHex)

	enc := make([]byte, 40)
	var i, o byte = 85, 246
	for c := 0; c < 39; c++ {
		u := o
		if (c & 1) != 0 {
			u = i
		}
		f := bits.OnesCount32(uint32(c))
		s := byte((int(payload[c])+21+f)&255) ^ u
		enc[1+c] = s
		if (c & 1) != 0 {
			i = s
		} else {
			o = s
		}
	}
	// Make e[0]^e[1]^e[2] == 0 so t >= 39
	enc[0] = enc[1] ^ enc[2]
	return base64.StdEncoding.EncodeToString(enc)
}

func encodeTestSpadeURL(plainURL string, keySeed []byte) string {
	a := sha512.Sum512(keySeed)
	mix := append(a[:], spadeURLSalt...)
	c := sha512.Sum512(mix)

	padLen := 16 - (len(plainURL) % 16)
	padded := make([]byte, len(plainURL)+padLen)
	copy(padded, plainURL)
	for i := len(plainURL); i < len(padded); i++ {
		padded[i] = byte(padLen)
	}

	blk, _ := aes.NewCipher(c[0:16])
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blk, c[16:32]).CryptBlocks(ct, padded)

	out := append([]byte{168, 0, 1, 0}, ct...)
	return base64.StdEncoding.EncodeToString(out)
}

func TestDecodeSpadeKey(t *testing.T) {
	want := "982735c47051044342ede4ef5d3e8cb5"
	spadeA := encodeTestSpadeA(want)
	got, err := DecodeSpadeKey(spadeA)
	if err != nil {
		t.Fatalf("DecodeSpadeKey error: %v", err)
	}
	if got != want {
		t.Fatalf("DecodeSpadeKey got=%s want=%s", got, want)
	}

	// Also accept URL-safe base64 without padding
	if _, err := DecodeSpadeKey("AA"); err == nil {
		t.Fatal("expected error for short spade_a")
	}
}

func TestDecodeSpadeURL(t *testing.T) {
	seed := []byte("0123456789abcdef0123456789abcdef")
	want := "https://v5-reading-video.qznovelvod.com/abc/def/video.mp4?dy_q=1794807027&n80=1"
	enc := encodeTestSpadeURL(want, seed)
	got, err := DecodeSpadeURL(enc, seed)
	if err != nil {
		t.Fatalf("DecodeSpadeURL error: %v", err)
	}
	if got != want {
		t.Fatalf("DecodeSpadeURL got=%s want=%s", got, want)
	}
}

func TestValidateAllowedURL(t *testing.T) {
	u, err := ValidateAllowedURL("https://v5-reading-video.qznovelvod.com/path?a=1", MediaCDNDomains)
	if err != nil {
		t.Fatalf("expected valid URL, got %v", err)
	}
	if u == "" {
		t.Fatal("expected non-empty normalized URL")
	}

	for _, bad := range []string{
		"http://v5-reading-video.qznovelvod.com/a",
		"https://user:pass@v5-reading-video.qznovelvod.com/a",
		"https://v5-reading-video.qznovelvod.com:8443/a",
		"https://evil.example.com/a",
	} {
		if _, err := ValidateAllowedURL(bad, MediaCDNDomains); err == nil {
			t.Fatalf("expected error for disallowed URL %s", bad)
		}
	}
}

func TestExtractThreeModesAndDeduplication(t *testing.T) {
	wantKey := "982735c47051044342ede4ef5d3e8cb5"
	spadeA := encodeTestSpadeA(wantKey)
	seed := []byte("0123456789abcdef0123456789abcdef")
	u1 := "https://v5-reading-video.qznovelvod.com/1/v.mp4"
	u2 := "https://v9-reading-video.fqnovelvod.com/2/v.mp4"

	// Mode 1: key_seed + spade_a
	modelEncrypted := &VideoModel{
		KeySeed:    base64.StdEncoding.EncodeToString(seed),
		ExpireTime: 1794808528,
		VideoList: VideoListField{
			{
				CodecType: "bytevc1",
				VWidth:    1080,
				VHeight:   1920,
				Size:      9131576,
				SpadeA:    spadeA,
				MainURL:   encodeTestSpadeURL(u1, seed),
				BackupURL: encodeTestSpadeURL(u1, seed), // duplicate should be deduped
			},
			{
				CodecType: "h264",
				Width:     720,
				Height:    1280,
				Size:      4000000,
				SpadeA:    spadeA,
				MainURL:   encodeTestSpadeURL(u2, seed),
			},
			{
				CodecType: "vp9",
				VWidth:    1080,
				VHeight:   1920,
				MainURL:   encodeTestSpadeURL(u1, seed),
			},
		},
	}

	streams, err := Extract(modelEncrypted, "7701680675979545625", 0)
	if err != nil {
		t.Fatalf("Extract encrypted error: %v", err)
	}
	if len(streams) != 2 {
		t.Fatalf("expected 2 deduplicated streams, got %d", len(streams))
	}
	if streams[0].Quality != 1080 || streams[0].Codec != "hevc" || streams[0].DecryptionKey != wantKey {
		t.Fatalf("unexpected first stream: %+v", streams[0])
	}
	if streams[0].ExpiresAt != 1794808528000 {
		t.Fatalf("unexpected expiresAt: %d", streams[0].ExpiresAt)
	}

	// Mode 2 & 3: plaintext https:// (escaped) and base64 URL without key_seed
	modelPlain := &VideoModel{
		VideoList: VideoListField{
			{
				CodecType: "h264",
				VWidth:    1080,
				VHeight:   1920,
				MainURL:   `https:\/\/v5-reading-video.qznovelvod.com\/plain\u0026a=1&amp;b=2`,
				BackupURL: base64.StdEncoding.EncodeToString([]byte(u2)),
			},
		},
	}
	plainStreams, err := Extract(modelPlain, "123", 0)
	if err != nil {
		t.Fatalf("Extract plain/base64 error: %v", err)
	}
	if len(plainStreams) != 2 {
		t.Fatalf("expected 2 streams, got %d", len(plainStreams))
	}
	if plainStreams[0].URL != "https://v5-reading-video.qznovelvod.com/plain&a=1&b=2" {
		t.Fatalf("unexpected unescaped URL: %s", plainStreams[0].URL)
	}
	if plainStreams[1].URL != u2 {
		t.Fatalf("unexpected base64 decoded URL: %s", plainStreams[1].URL)
	}
}

func TestVideoListFieldArrayAndObject(t *testing.T) {
	arrJSON := `{"expire_time":1794833209,"video_duration":114.01,"video_list":[{"codec_type":"hevc","vwidth":1080,"vheight":1920,"main_url":"https://v5-reading-video.qznovelvod.com/a.mp4"}]}`
	var mArr VideoModel
	if err := json.Unmarshal([]byte(arrJSON), &mArr); err != nil {
		t.Fatalf("unmarshal array video_list failed: %v", err)
	}
	if len(mArr.VideoList) != 1 || mArr.ExpireTime != 1794833209 || mArr.VideoDuration != 114.01 {
		t.Fatalf("unexpected array model: %+v", mArr)
	}

	objJSON := `{"expire_time":"1794833209","video_list":{"video_1":{"codec_type":"h264","vwidth":720,"vheight":1280,"main_url":"https://v5-reading-video.qznovelvod.com/b.mp4"}}}`
	var mObj VideoModel
	if err := json.Unmarshal([]byte(objJSON), &mObj); err != nil {
		t.Fatalf("unmarshal object video_list failed: %v", err)
	}
	if len(mObj.VideoList) != 1 || mObj.ExpireTime != 1794833209 {
		t.Fatalf("unexpected object model: %+v", mObj)
	}
}
