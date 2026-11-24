package signer

import (
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type goldenVectors struct {
	Selectors []struct {
		Selector    int    `json:"selector"`
		Query       string `json:"query"`
		BodyHashHex string `json:"bodyHashHex"`
		Epoch       uint32 `json:"epoch"`
		MaterialHex string `json:"materialHex"`
		Nonce       uint32 `json:"nonce"`
		ProtoHex    string `json:"protoHex"`
		NormalFalse struct {
			QueryHashHex   string `json:"queryHashHex"`
			Selector       int    `json:"selector"`
			MixedDigestHex string `json:"mixedDigestHex"`
			PrefixFlag     byte   `json:"prefixFlag"`
			StreamSeedHex  string `json:"streamSeedHex"`
			StreamKeyHex   string `json:"streamKeyHex"`
			XiHex          string `json:"xiHex"`
		} `json:"normalJniFalse"`
		NormalTrue struct {
			QueryHashHex   string `json:"queryHashHex"`
			Selector       int    `json:"selector"`
			MixedDigestHex string `json:"mixedDigestHex"`
			PrefixFlag     byte   `json:"prefixFlag"`
			StreamSeedHex  string `json:"streamSeedHex"`
			StreamKeyHex   string `json:"streamKeyHex"`
			XiHex          string `json:"xiHex"`
		} `json:"normalJniTrue"`
	} `json:"selectors"`
	Signs []struct {
		URL        string  `json:"url"`
		BodyBase64 *string `json:"bodyBase64"`
		Epoch      uint32  `json:"epoch"`
		Device     struct {
			IID      string `json:"iid"`
			DeviceID string `json:"device_id"`
			CDID     string `json:"cdid"`
		} `json:"device"`
		Randoms          [3]uint32         `json:"randoms"`
		GorgonRand       uint16            `json:"gorgonRand"`
		HeliosRand       uint32            `json:"heliosRand"`
		Counter          int32             `json:"counter"`
		NaturalSelector  int               `json:"naturalSelector"`
		NaturalDigestHex string            `json:"naturalDigestHex"`
		ProtoHex         string            `json:"protoHex"`
		Headers          map[string]string `json:"headers"`
		RealHeaders      map[string]string `json:"realHeaders"`
	} `json:"signs"`
}

type fixedRand struct {
	u32    [3]uint32
	u32Idx int
	u16    uint16
	u32Big uint32
}

func (f *fixedRand) Uint32() (uint32, error) {
	v := f.u32[f.u32Idx]
	f.u32Idx++
	return v, nil
}
func (f *fixedRand) Uint16() (uint16, error)    { return f.u16, nil }
func (f *fixedRand) Uint32Big() (uint32, error) { return f.u32Big, nil }

func loadGoldenVectors(t *testing.T) goldenVectors {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_vectors.json"))
	if err != nil {
		t.Fatalf("read golden_vectors.json: %v", err)
	}
	var gv goldenVectors
	if err := json.Unmarshal(raw, &gv); err != nil {
		t.Fatalf("unmarshal golden_vectors.json: %v", err)
	}
	return gv
}

func TestComponentsAll16Selectors(t *testing.T) {
	gv := loadGoldenVectors(t)
	if len(gv.Selectors) != 16 {
		t.Fatalf("expected 16 selector vectors, got %d", len(gv.Selectors))
	}

	for _, sv := range gv.Selectors {
		bodyHash, err := hex.DecodeString(sv.BodyHashHex)
		if err != nil {
			t.Fatalf("decode bodyHash: %v", err)
		}
		mat, err := hex.DecodeString(sv.MaterialHex)
		if err != nil {
			t.Fatalf("decode material: %v", err)
		}
		proto, err := hex.DecodeString(sv.ProtoHex)
		if err != nil {
			t.Fatalf("decode proto: %v", err)
		}

		for _, tc := range []struct {
			name      string
			normalJni bool
			want      struct {
				QueryHashHex   string
				Selector       int
				MixedDigestHex string
				PrefixFlag     byte
				StreamSeedHex  string
				StreamKeyHex   string
				XiHex          string
			}
		}{
			{
				name:      "normalJni=false",
				normalJni: false,
				want: struct {
					QueryHashHex   string
					Selector       int
					MixedDigestHex string
					PrefixFlag     byte
					StreamSeedHex  string
					StreamKeyHex   string
					XiHex          string
				}(sv.NormalFalse),
			},
			{
				name:      "normalJni=true",
				normalJni: true,
				want: struct {
					QueryHashHex   string
					Selector       int
					MixedDigestHex string
					PrefixFlag     byte
					StreamSeedHex  string
					StreamKeyHex   string
					XiHex          string
				}(sv.NormalTrue),
			},
		} {
			sel := sv.Selector
			got, err := components(componentsInput{
				QueryBytes:       []byte(sv.Query),
				BodyHash:         bodyHash,
				Epoch:            sv.Epoch,
				NormalJNI:        tc.normalJni,
				Material:         mat,
				Nonce:            sv.Nonce,
				Proto:            proto,
				SelectorOverride: &sel,
			})
			if err != nil {
				t.Fatalf("selector=%d (%s) error: %v", sel, tc.name, err)
			}
			if got.Selector != tc.want.Selector {
				t.Fatalf("selector=%d (%s) got selector=%d want=%d", sel, tc.name, got.Selector, tc.want.Selector)
			}
			if hex.EncodeToString(got.QueryHash) != tc.want.QueryHashHex {
				t.Fatalf("selector=%d (%s) queryHash mismatch:\ngot:  %s\nwant: %s", sel, tc.name, hex.EncodeToString(got.QueryHash), tc.want.QueryHashHex)
			}
			if hex.EncodeToString(got.MixedDigest) != tc.want.MixedDigestHex {
				t.Fatalf("selector=%d (%s) mixedDigest mismatch:\ngot:  %s\nwant: %s", sel, tc.name, hex.EncodeToString(got.MixedDigest), tc.want.MixedDigestHex)
			}
			if got.PrefixFlag != tc.want.PrefixFlag {
				t.Fatalf("selector=%d (%s) prefixFlag got=%d want=%d", sel, tc.name, got.PrefixFlag, tc.want.PrefixFlag)
			}
			if hex.EncodeToString(got.StreamSeed) != tc.want.StreamSeedHex {
				t.Fatalf("selector=%d (%s) streamSeed mismatch", sel, tc.name)
			}
			if hex.EncodeToString(got.StreamKey) != tc.want.StreamKeyHex {
				t.Fatalf("selector=%d (%s) streamKey mismatch:\ngot:  %s\nwant: %s", sel, tc.name, hex.EncodeToString(got.StreamKey), tc.want.StreamKeyHex)
			}
			if hex.EncodeToString(got.Xi) != tc.want.XiHex {
				t.Fatalf("selector=%d (%s) xi mismatch:\ngot:  %s\nwant: %s", sel, tc.name, hex.EncodeToString(got.Xi), tc.want.XiHex)
			}
		}
	}
}

func TestSignHeadersGoldenParity(t *testing.T) {
	gv := loadGoldenVectors(t)
	if len(gv.Signs) == 0 {
		t.Fatal("no sign vectors")
	}

	for idx, sv := range gv.Signs {
		var body []byte
		if sv.BodyBase64 != nil {
			var err error
			body, err = base64.StdEncoding.DecodeString(*sv.BodyBase64)
			if err != nil {
				t.Fatalf("sign[%d] decode bodyBase64: %v", idx, err)
			}
		}

		rng := &fixedRand{
			u32:    sv.Randoms,
			u16:    sv.GorgonRand,
			u32Big: sv.HeliosRand,
		}
		prof := BuiltInProfile(rng)
		dev := Device{
			IID:      sv.Device.IID,
			DeviceID: sv.Device.DeviceID,
			CDID:     sv.Device.CDID,
		}

		prod, err := prof.Produce(ProduceInput{
			URL:     sv.URL,
			Body:    body,
			Epoch:   sv.Epoch,
			Device:  dev,
			Randoms: sv.Randoms,
			Counter: &sv.Counter,
		})
		if err != nil {
			t.Fatalf("sign[%d] Produce error: %v", idx, err)
		}
		if hex.EncodeToString(prod.Proto) != sv.ProtoHex {
			t.Fatalf("sign[%d] proto mismatch:\ngot:  %s\nwant: %s", idx, hex.EncodeToString(prod.Proto), sv.ProtoHex)
		}
		if prod.Signature != sv.Headers["x-medusa"] {
			t.Fatalf("sign[%d] x-medusa mismatch:\ngot:  %s\nwant: %s", idx, prod.Signature, sv.Headers["x-medusa"])
		}

		// Reset counter to 0 (so Sign increments to 1) and test full Sign()
		prof2 := BuiltInProfile(rng)
		gotHeaders, err := prof2.Sign(SignInput{
			URL:    sv.URL,
			Body:   body,
			Epoch:  sv.Epoch,
			Device: dev,
		})
		if err != nil {
			t.Fatalf("sign[%d] Sign error: %v", idx, err)
		}
		gotMap := gotHeaders.Map()
		for k, wantVal := range sv.Headers {
			if gotMap[k] != wantVal {
				t.Fatalf("sign[%d] header %s mismatch:\ngot:  %s\nwant: %s", idx, k, gotMap[k], wantVal)
			}
		}
		for k, wantReal := range sv.RealHeaders {
			if gotMap[k] != wantReal {
				t.Fatalf("sign[%d] real header %s mismatch:\ngot:  %s\nwant: %s", idx, k, gotMap[k], wantReal)
			}
		}
	}
}

func TestEncodeQueryJS(t *testing.T) {
	got := EncodeQuery([]QueryKV{
		{Key: "a", Value: "hello world!~*'()"},
		{Key: "b", Value: "1280*2772"},
	})
	want := "a=hello%20world%21~*%27%28%29&b=1280*2772"
	if got != want {
		t.Fatalf("EncodeQuery mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestBuildSignedRequest(t *testing.T) {
	prof := BuiltInProfile(CryptoRand{})
	dev := Device{
		IID:      "4069772115858074",
		DeviceID: "4069772115853978",
		CDID:     "5ecfd14f-e6e9-42f8-89ac-bb8e9ab842aa",
	}
	now := time.Unix(1794807027, 688*int64(time.Millisecond))

	// GET (empty body -> x-ss-stub must be absent)
	reqGet, err := BuildSignedRequest(prof, nil, "https://reading.snssdk.com/reading/bookapi/bookmall/tab/v", []QueryKV{
		{"tab_type", "8"},
	}, dev, now)
	if err != nil {
		t.Fatalf("BuildSignedRequest GET error: %v", err)
	}
	if _, exists := reqGet.Headers["x-ss-stub"]; exists {
		t.Fatal("expected x-ss-stub to be omitted on nil body")
	}
	for _, h := range []string{"x-gorgon", "x-khronos", "x-ladon", "x-argus", "x-helios", "x-medusa"} {
		if reqGet.Headers[h] == "" {
			t.Fatalf("missing signature header %s", h)
		}
	}

	// POST (non-empty body -> x-ss-stub must match wire-sample)
	body := []byte(`{"series_id":"7701678491724418073"}`)
	reqPost, err := BuildSignedRequest(prof, body, "https://reading.snssdk.com/novel/player/video_detail/v1/", nil, dev, now)
	if err != nil {
		t.Fatalf("BuildSignedRequest POST error: %v", err)
	}
	if reqPost.Headers["x-ss-stub"] != "F0C96E4FE1E592E0FD0AC01D7B512A42" {
		t.Fatalf("x-ss-stub mismatch: got %s", reqPost.Headers["x-ss-stub"])
	}
}
