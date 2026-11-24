package resolver

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

// MediaCDNDomains are the permitted CDN domain suffixes for extracted stream URLs.
var MediaCDNDomains = []string{"qznovelvod.com", "fqnovelvod.com", "douyinvod.com"}

// VideoListField unmarshals video_list whether upstream sends a JSON array or a JSON object map.
type VideoListField []VideoItem

func (v *VideoListField) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*v = nil
		return nil
	}
	if trimmed[0] == '[' {
		var arr []VideoItem
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return err
		}
		*v = arr
		return nil
	}
	var obj map[string]VideoItem
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return err
	}
	out := make([]VideoItem, 0, len(obj))
	for _, item := range obj {
		out = append(out, item)
	}
	*v = out
	return nil
}

// VideoModel mirrors the upstream video_model payload.
type VideoModel struct {
	VideoID       string          `json:"video_id"`
	KeySeed       string          `json:"key_seed"`
	FallbackAPI   json.RawMessage `json:"fallback_api"`
	DynamicVideo  json.RawMessage `json:"dynamic_video"`
	VideoDuration float64         `json:"video_duration"`
	ExpireTime    FlexInt64       `json:"expire_time"`
	URLExpire     FlexInt64       `json:"url_expire"`
	VideoList     VideoListField  `json:"video_list"`

	// Internal override calculated from response entry expire_time.
	InternalExpiresAtMs int64 `json:"-"`
}

// VideoItem mirrors an entry in video_model.video_list.
type VideoItem struct {
	MainURL       string   `json:"main_url"`
	BackupURL     string   `json:"backup_url"`
	BackupURL1    string   `json:"backup_url_1"`
	BackupURL2    string   `json:"backup_url_2"`
	BackupURL3    string   `json:"backup_url_3"`
	BackupURLList []string `json:"backup_url_list"`
	SpadeA        string   `json:"spade_a"`
	EncryptInfo   *struct {
		SpadeA string `json:"spade_a"`
	} `json:"encrypt_info"`
	CodecType   string    `json:"codec_type"`
	VType       string    `json:"vtype"`
	VWidth      FlexInt   `json:"vwidth"`
	VHeight     FlexInt   `json:"vheight"`
	Width       FlexInt   `json:"width"`
	Height      FlexInt   `json:"height"`
	QualityDesc string    `json:"quality_desc"`
	Definition  string    `json:"definition"`
	Size        FlexInt64 `json:"size"`
	VideoMeta   *struct {
		CodecType  string    `json:"codec_type"`
		VWidth     FlexInt   `json:"vwidth"`
		VHeight    FlexInt   `json:"vheight"`
		Width      FlexInt   `json:"width"`
		Height     FlexInt   `json:"height"`
		Definition string    `json:"definition"`
		Size       FlexInt64 `json:"size"`
	} `json:"video_meta"`
}

// FlexInt parses JSON numbers or numeric strings into int.
type FlexInt int

func (f *FlexInt) UnmarshalJSON(data []byte) error {
	var n float64
	if err := json.Unmarshal(data, &n); err == nil {
		if !math.IsNaN(n) && !math.IsInf(n, 0) {
			*f = FlexInt(int(n))
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if v, err := strconv.Atoi(s); err == nil {
			*f = FlexInt(v)
		}
	}
	return nil
}

// FlexInt64 parses JSON numbers or numeric strings into int64.
type FlexInt64 int64

func (f *FlexInt64) UnmarshalJSON(data []byte) error {
	var n int64
	if err := json.Unmarshal(data, &n); err == nil {
		*f = FlexInt64(n)
		return nil
	}
	var fl float64
	if err := json.Unmarshal(data, &fl); err == nil {
		if !math.IsNaN(fl) && !math.IsInf(fl, 0) {
			*f = FlexInt64(int64(fl))
		}
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		if v, err := strconv.ParseInt(s, 10, 64); err == nil {
			*f = FlexInt64(v)
		}
	}
	return nil
}

// AppModelExpiry computes the minimum valid millisecond expiration timestamp (REF L1017).
func AppModelExpiry(model *VideoModel, entryExpire int64) int64 {
	candidates := []int64{entryExpire}
	if model != nil {
		candidates = append(candidates, model.InternalExpiresAtMs, int64(model.URLExpire), int64(model.ExpireTime))
	}
	var best int64
	for _, v := range candidates {
		if v < 1000000000 {
			continue
		}
		if v < 1000000000000 {
			v *= 1000
		}
		if v > 0 && v <= 8640000000000000 {
			if best == 0 || v < best {
				best = v
			}
		}
	}
	return best
}
