package signer

import (
	"crypto/md5"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// AppUserAgent matches REF L1008 dt / wire-samples.json.
const AppUserAgent = "com.phoenix.read/71332 (Linux; U; Android 16; zh_CN; 25053RT47C; Build/BP2A.250605.031.A3; Cronet/TTNetVersion:04657795 2026-01-23 QuicVersion:c67e9834 2025-09-08)"

// SignedRequest holds the fully constructed URL, headers, and optional body for an upstream call.
type SignedRequest struct {
	URL     string
	Headers map[string]string
	Body    []byte
}

// CheckSelectorMod3 implements to(url, body, epoch) from REF L814.
// Returns selector % 3 (0, 1, or 2).
func CheckSelectorMod3(rawURL string, body []byte, epoch uint32) int {
	var bodyHash [16]byte
	if len(body) > 0 {
		bodyHash = md5.Sum(body)
	}
	q := rawURL
	if idx := strings.IndexByte(rawURL, '?'); idx >= 0 {
		q = rawURL[idx+1:]
	}
	qHash := sm3Sum([]byte(q))
	sel := computeSelector(qHash[:], bodyHash[:], epoch)
	return sel % 3
}

// BuildSignedRequest implements Ht(body, endpoint, extraParams, device) from REF L1008.
func BuildSignedRequest(prof *Profile, body []byte, endpoint string, extraParams []QueryKV, dev Device, now time.Time) (SignedRequest, error) {
	if prof == nil {
		return SignedRequest{}, ErrMissingConfiguration
	}
	if now.IsZero() {
		now = time.Now()
	}
	epoch := uint32(now.Unix())
	baseMs := now.UnixMilli()

	var bodyCopy []byte
	if body != nil {
		bodyCopy = body
	}

	var rticket int64
	var fullURL string

	for x := int64(0); x < 32; x++ {
		rticket = baseMs + x
		pairs := make([]QueryKV, 0, 30+len(extraParams))
		pairs = append(pairs,
			QueryKV{"ac", "wifi"},
			QueryKV{"channel", "update_64"},
			QueryKV{"aid", "8662"},
			QueryKV{"app_name", "novelread"},
			QueryKV{"version_code", "73932"},
			QueryKV{"version_name", "7.3.9.32"},
			QueryKV{"device_platform", "android"},
			QueryKV{"os", "android"},
			QueryKV{"ssmix", "a"},
			QueryKV{"device_type", "25053RT47C"},
			QueryKV{"device_brand", "Redmi"},
			QueryKV{"language", "zh"},
			QueryKV{"os_api", "36"},
			QueryKV{"os_version", "16"},
			QueryKV{"manifest_version_code", "73932"},
			QueryKV{"resolution", "1280*2772"},
			QueryKV{"dpi", "520"},
			QueryKV{"update_version_code", "73932"},
			QueryKV{"host_abi", "arm64-v8a"},
			QueryKV{"dragon_device_type", "phone"},
			QueryKV{"pv_player", "73932"},
			QueryKV{"compliance_status", "0"},
			QueryKV{"need_personal_recommend", "1"},
			QueryKV{"player_so_load", "1"},
			QueryKV{"is_android_pad_screen", "0"},
			QueryKV{"iid", dev.IID},
			QueryKV{"device_id", dev.DeviceID},
			QueryKV{"cdid", dev.CDID},
		)
		pairs = append(pairs, extraParams...)
		pairs = append(pairs,
			QueryKV{"ts", strconv.FormatUint(uint64(epoch), 10)},
			QueryKV{"_rticket", strconv.FormatInt(rticket, 10)},
		)
		qs := EncodeQuery(pairs)
		fullURL = endpoint + "?" + qs
		if CheckSelectorMod3(fullURL, bodyCopy, epoch) != 1 {
			break
		}
	}

	sigHeaders, err := prof.Sign(SignInput{
		URL:    fullURL,
		Body:   bodyCopy,
		Epoch:  epoch,
		Device: dev,
	})
	if err != nil {
		return SignedRequest{}, err
	}

	headers := map[string]string{
		"User-Agent":                AppUserAgent,
		"Accept":                    "application/json; charset=utf-8,application/x-protobuf",
		"Content-Type":              "application/json; charset=UTF-8",
		"x-xs-from-web":             "0",
		"x-ss-req-ticket":           strconv.FormatInt(rticket, 10),
		"x-tt-request-tag":          "t=0;n=0",
		"sdk-version":               "2",
		"passport-sdk-version":      "50561",
		"x-vc-bdturing-sdk-version": "3.7.2.cn",
		"x-tt-dt":                   "",
	}
	for k, v := range sigHeaders.Map() {
		headers[k] = v
	}
	if len(bodyCopy) > 0 {
		sum := md5.Sum(bodyCopy)
		headers["x-ss-stub"] = strings.ToUpper(hex.EncodeToString(sum[:]))
	}

	return SignedRequest{
		URL:     fullURL,
		Headers: headers,
		Body:    bodyCopy,
	}, nil
}
