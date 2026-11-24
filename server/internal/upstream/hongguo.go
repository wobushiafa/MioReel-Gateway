package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/signer"
)

// AppModelRoute describes one video_model upstream route (REF L1011-1016).
type AppModelRoute struct {
	URL    string
	Single bool
}

var DefaultAppModelRoutes = []AppModelRoute{
	{URL: "https://api5-normal-sinfonlineb.fqnovel.com/novel/player/multi_video_model/v1/", Single: false},
	{URL: "https://reading.snssdk.com/novel/player/multi_video_model/v1/", Single: false},
	{URL: "https://reading.snssdk.com/novel/player/video_model/v1/", Single: true},
	{URL: "https://api5-normal.fqnovel.com/novel/player/multi_video_model/v1/", Single: false},
}

var allowedDiscoveryPaths = map[string]struct{}{
	"/reading/bookapi/search/cue/v":           {},
	"/reading/bookapi/search/suggest/v":       {},
	"/reading/bookapi/plan/v":                 {},
	"/reading/bookapi/bookmall/tab/v":         {},
	"/reading/bookapi/bookmall/cell/change/v": {},
}

// HongguoClient wraps all signed Hongguo API calls, route fallback, and health tracking.
type HongguoClient struct {
	http    *Client
	metrics *observability.Metrics

	mu                sync.RWMutex
	preferredRoute    *AppModelRoute
	preferredUntil    time.Time
	lastSuccessAt     time.Time
	consecutiveFailed int64
}

func NewHongguoClient(httpClient *Client, metrics *observability.Metrics) *HongguoClient {
	if httpClient == nil {
		httpClient = NewClient()
	}
	return &HongguoClient{
		http:    httpClient,
		metrics: metrics,
	}
}

func (c *HongguoClient) HealthStats() (lastSuccessAt time.Time, consecutiveFailures int64) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastSuccessAt, c.consecutiveFailed
}

func (c *HongguoClient) recordSuccess() {
	c.mu.Lock()
	c.lastSuccessAt = time.Now().UTC()
	c.consecutiveFailed = 0
	c.mu.Unlock()
}

func (c *HongguoClient) recordFailure() {
	c.mu.Lock()
	c.consecutiveFailed++
	c.mu.Unlock()
}

func (c *HongguoClient) orderedRoutes() []AppModelRoute {
	c.mu.RLock()
	pref := c.preferredRoute
	until := c.preferredUntil
	c.mu.RUnlock()

	if pref == nil || !time.Now().Before(until) {
		return append([]AppModelRoute(nil), DefaultAppModelRoutes...)
	}
	out := make([]AppModelRoute, 0, len(DefaultAppModelRoutes))
	out = append(out, *pref)
	for _, r := range DefaultAppModelRoutes {
		if r.URL != pref.URL {
			out = append(out, r)
		}
	}
	return out
}

func (c *HongguoClient) setPreferredRoute(r AppModelRoute) {
	c.mu.Lock()
	c.preferredRoute = &AppModelRoute{URL: r.URL, Single: r.Single}
	c.preferredUntil = time.Now().Add(5 * time.Minute)
	c.mu.Unlock()
}

func (c *HongguoClient) clearPreferredRoute(r AppModelRoute) {
	c.mu.Lock()
	if c.preferredRoute != nil && c.preferredRoute.URL == r.URL {
		c.preferredRoute = nil
		c.preferredUntil = time.Time{}
	}
	c.mu.Unlock()
}

type bizParam struct {
	DetailPageVersion      int  `json:"detail_page_version"`
	DeviceLevel            int  `json:"device_level"`
	DisableDiggStat        bool `json:"disable_digg_stat"`
	NeedAllVideoDefinition bool `json:"need_all_video_definition"`
	NeedMP4Align           bool `json:"need_mp4_align"`
	UseOSPlayer            bool `json:"use_os_player"`
	UseServerDNS           bool `json:"use_server_dns"`
	VideoPlatform          int  `json:"video_platform"`
}

func defaultBizParam() bizParam {
	return bizParam{
		DetailPageVersion:      0,
		DeviceLevel:            3,
		DisableDiggStat:        false,
		NeedAllVideoDefinition: true,
		NeedMP4Align:           false,
		UseOSPlayer:            false,
		UseServerDNS:           false,
		VideoPlatform:          1024,
	}
}

type videoModelResponse struct {
	Code    json.RawMessage            `json:"code"`
	CapCode json.RawMessage            `json:"Code"`
	Data    map[string]json.RawMessage `json:"data"`
}

type videoModelEntry struct {
	ExpireTime json.RawMessage `json:"expire_time"`
	VideoModel json.RawMessage `json:"video_model"`
}

func parseJSONCode(raws ...json.RawMessage) int64 {
	for _, r := range raws {
		if len(r) == 0 {
			continue
		}
		var n int64
		if err := json.Unmarshal(r, &n); err == nil {
			return n
		}
		var s string
		if err := json.Unmarshal(r, &s); err == nil {
			if v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				return v
			}
		}
	}
	return 0
}

func parseJSONInt64(r json.RawMessage) int64 {
	var n int64
	if err := json.Unmarshal(r, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(r, &s); err == nil {
		v, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		return v
	}
	return 0
}

// FetchVideoModel implements requestAppVideoModel (REF L1025-1055) with 4-route fallback and 5-min sticky preference.
// Returns (model, routeHost, error).
func (c *HongguoClient) FetchVideoModel(ctx context.Context, prof *signer.Profile, dev signer.Device, videoID string, deadline time.Time) (*resolver.VideoModel, string, error) {
	routes := c.orderedRoutes()
	var lastErr error

	for idx, route := range routes {
		if ctx.Err() != nil {
			return nil, "", &UpstreamError{Code: "CANCELLED", Message: "cancelled", Err: ctx.Err()}
		}
		if !time.Now().Before(deadline) {
			break
		}
		if idx > 0 && c.metrics != nil {
			c.metrics.IncUpstreamFallback()
		}

		routeHost := route.URL
		if u, err := url.Parse(route.URL); err == nil {
			routeHost = u.Host
		}

		var bodyPayload any
		if route.Single {
			bodyPayload = map[string]any{
				"video_id":     videoID,
				"content_type": 1,
				"biz_param":    defaultBizParam(),
			}
		} else {
			bodyPayload = map[string]any{
				"biz_param": defaultBizParam(),
				"mixed_video_id_map": map[string][]string{
					"1004": {videoID},
				},
			}
		}
		bodyBytes, err := json.Marshal(bodyPayload)
		if err != nil {
			return nil, routeHost, &UpstreamError{Code: "INVALID_RESPONSE", Message: err.Error(), Err: err}
		}

		signed, err := signer.BuildSignedRequest(prof, bodyBytes, route.URL, nil, dev, time.Now())
		if err != nil {
			return nil, routeHost, err
		}

		perReqDeadline := time.Now().Add(3500 * time.Millisecond)
		if deadline.Before(perReqDeadline) {
			perReqDeadline = deadline
		}

		text, err := c.http.DoText(ctx, "POST", signed.URL, signed.Headers, signed.Body, perReqDeadline)
		if err != nil {
			lastErr = err
			if c.metrics != nil {
				var ue *UpstreamError
				code := "ERROR"
				if errors.As(err, &ue) {
					code = ue.Code
				}
				c.metrics.IncUpstreamRequest(routeHost, code)
			}
			if !IsRecoverableModelError(err) || ctx.Err() != nil {
				c.recordFailure()
				return nil, routeHost, err
			}
			c.clearPreferredRoute(route)
			continue
		}

		var resp videoModelResponse
		if err := json.Unmarshal([]byte(text), &resp); err != nil || resp.Data == nil {
			lastErr = &UpstreamError{Code: "INVALID_RESPONSE", Message: "video_model envelope parse failed", Err: err}
			if c.metrics != nil {
				c.metrics.IncUpstreamRequest(routeHost, "INVALID_RESPONSE")
			}
			c.clearPreferredRoute(route)
			continue
		}

		bizCode := parseJSONCode(resp.CapCode, resp.Code)
		if bizCode != 0 {
			if c.metrics != nil {
				c.metrics.IncUpstreamRequest(routeHost, "BUSINESS_REJECTED")
			}
			c.recordFailure()
			return nil, routeHost, &UpstreamError{Code: "BUSINESS_REJECTED", Message: "upstream video_model rejected request"}
		}

		var entryRaw json.RawMessage
		if route.Single {
			entryRaw, _ = json.Marshal(resp.Data)
		} else {
			entryRaw = resp.Data[videoID]
		}
		if len(entryRaw) == 0 || string(entryRaw) == "null" {
			lastErr = &UpstreamError{Code: "MISSING_MODEL", Message: "missing entry for video_id"}
			if c.metrics != nil {
				c.metrics.IncUpstreamRequest(routeHost, "MISSING_MODEL")
			}
			c.clearPreferredRoute(route)
			continue
		}

		var ent videoModelEntry
		if err := json.Unmarshal(entryRaw, &ent); err != nil || len(ent.VideoModel) == 0 || string(ent.VideoModel) == "null" {
			lastErr = &UpstreamError{Code: "MISSING_MODEL", Message: "missing video_model field"}
			if c.metrics != nil {
				c.metrics.IncUpstreamRequest(routeHost, "MISSING_MODEL")
			}
			c.clearPreferredRoute(route)
			continue
		}

		model, err := decodeVideoModelField(ent.VideoModel)
		if err != nil {
			lastErr = &UpstreamError{Code: "INVALID_RESPONSE", Message: "invalid video_model JSON", Err: err}
			if c.metrics != nil {
				c.metrics.IncUpstreamRequest(routeHost, "INVALID_RESPONSE")
			}
			c.clearPreferredRoute(route)
			continue
		}

		entExp := parseJSONInt64(ent.ExpireTime)
		model.InternalExpiresAtMs = resolver.AppModelExpiry(model, entExp)

		c.setPreferredRoute(route)
		c.recordSuccess()
		if c.metrics != nil {
			c.metrics.IncUpstreamRequest(routeHost, "0")
		}
		return model, routeHost, nil
	}

	c.recordFailure()
	if ue, ok := lastErr.(*UpstreamError); ok {
		cp := *ue
		cp.RoutesExhausted = true
		return nil, "", &cp
	}
	return nil, "", &UpstreamError{Code: "ETIMEDOUT", Message: "all video_model routes timed out or exhausted", RoutesExhausted: true}
}

func decodeVideoModelField(raw json.RawMessage) (*resolver.VideoModel, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, `"`) {
		var innerStr string
		if err := json.Unmarshal(raw, &innerStr); err != nil {
			return nil, err
		}
		trimmed = QuoteLongJSONIDs(innerStr)
	}
	var m resolver.VideoModel
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// FetchFallbackVideoModel implements the fallback_api path in oo() (REF L1082-1099).
func (c *HongguoClient) FetchFallbackVideoModel(ctx context.Context, fallbackRaw json.RawMessage, videoID string, deadline time.Time) (*resolver.VideoModel, error) {
	fbURL := extractFallbackURL(fallbackRaw)
	if fbURL == "" {
		return nil, &UpstreamError{Code: "MISSING_MODEL", Message: "empty fallback_api"}
	}
	validURL, err := resolver.ValidateAllowedURL(fbURL, UpstreamAPIDomains)
	if err != nil {
		if de, ok := err.(*resolver.DomainError); ok {
			return nil, &UpstreamError{Code: de.Code, Message: de.Message, Err: err}
		}
		return nil, &UpstreamError{Code: "SECURITY_BLOCKED", Message: err.Error(), Err: err}
	}

	u, err := url.Parse(validURL)
	if err != nil {
		return nil, &UpstreamError{Code: "INVALID_RESPONSE", Message: err.Error(), Err: err}
	}
	q := u.Query()
	q.Del("force_fids")
	q.Set("format_type", "mp4")
	q.Set("codec_type", "1")
	u.RawQuery = q.Encode()

	text, err := c.http.DoText(ctx, "GET", u.String(), map[string]string{"User-Agent": signer.AppUserAgent}, nil, deadline)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Code      json.RawMessage `json:"code"`
		CapCode   json.RawMessage `json:"Code"`
		VideoInfo *struct {
			Code    json.RawMessage      `json:"code"`
			CapCode json.RawMessage      `json:"Code"`
			Data    *resolver.VideoModel `json:"data"`
		} `json:"video_info"`
	}
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		return nil, &UpstreamError{Code: "INVALID_RESPONSE", Message: "invalid fallback_api JSON", Err: err}
	}
	if parseJSONCode(parsed.CapCode, parsed.Code) != 0 {
		return nil, &UpstreamError{Code: "BUSINESS_REJECTED", Message: "fallback_api business rejected"}
	}
	if parsed.VideoInfo != nil && parseJSONCode(parsed.VideoInfo.CapCode, parsed.VideoInfo.Code) != 0 {
		return nil, &UpstreamError{Code: "BUSINESS_REJECTED", Message: "fallback_api video_info rejected"}
	}
	if parsed.VideoInfo == nil || parsed.VideoInfo.Data == nil {
		return nil, &UpstreamError{Code: "MISSING_MODEL", Message: "fallback_api missing video_info.data"}
	}
	return parsed.VideoInfo.Data, nil
}

func extractFallbackURL(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		// Might be JSON string containing {"fallback_api":"..."} or raw URL
		sTrim := strings.TrimSpace(s)
		if strings.HasPrefix(sTrim, "{") {
			var obj struct {
				FallbackAPI string `json:"fallback_api"`
			}
			if json.Unmarshal([]byte(sTrim), &obj) == nil && obj.FallbackAPI != "" {
				return obj.FallbackAPI
			}
		}
		return sTrim
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return strings.TrimSpace(arr[0])
	}
	var obj struct {
		FallbackAPI string `json:"fallback_api"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return strings.TrimSpace(obj.FallbackAPI)
	}
	return ""
}

// AppDetailVideoData represents the parsed video_data from /novel/player/video_detail/v1/.
type AppDetailVideoData struct {
	SeriesID           string            `json:"series_id"`
	SeriesIDStr        string            `json:"series_id_str"`
	SeriesTitle        string            `json:"series_title"`
	SeriesName         string            `json:"series_name"`
	Title              string            `json:"title"`
	SeriesCover        string            `json:"series_cover"`
	Cover              string            `json:"cover"`
	SeriesIntro        string            `json:"series_intro"`
	VideoDesc          string            `json:"video_desc"`
	Intro              string            `json:"intro"`
	Desc               string            `json:"desc"`
	EpisodeCnt         resolver.FlexInt  `json:"episode_cnt"`
	AccessibleEpisodes resolver.FlexInt  `json:"accessible_episode_cnt"`
	AccessibleCount    resolver.FlexInt  `json:"accessible_count"`
	VideoList          []AppEpisodeEntry `json:"video_list"`
}

type AppEpisodeEntry struct {
	VID         string `json:"vid"`
	VideoID     string `json:"video_id"`
	Title       string `json:"title"`
	DisablePlay bool   `json:"disable_play"`
}

// FetchDetailRaw implements fetchAppDetailRaw (REF L1166).
func (c *HongguoClient) FetchDetailRaw(ctx context.Context, prof *signer.Profile, dev signer.Device, seriesID string, deadline time.Time) (*AppDetailVideoData, error) {
	bodyBytes, _ := json.Marshal(map[string]string{"series_id": seriesID})
	signed, err := signer.BuildSignedRequest(prof, bodyBytes, "https://reading.snssdk.com/novel/player/video_detail/v1/", nil, dev, time.Now())
	if err != nil {
		return nil, err
	}
	signed.Headers["lc"] = "101"

	text, err := c.http.DoText(ctx, "POST", signed.URL, signed.Headers, signed.Body, deadline)
	if err != nil {
		c.recordFailure()
		return nil, err
	}

	var res struct {
		Code json.RawMessage `json:"code"`
		Data struct {
			VideoData *AppDetailVideoData `json:"video_data"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		return nil, &UpstreamError{Code: "INVALID_RESPONSE", Message: "video_detail parse failed", Err: err}
	}
	if parseJSONCode(res.Code) != 0 {
		return nil, &UpstreamError{Code: "DETAIL_REJECTED", Message: "video_detail rejected"}
	}
	if res.Data.VideoData == nil {
		return nil, &UpstreamError{Code: "MISSING_EPISODE", Message: "video_detail missing video_data"}
	}
	c.recordSuccess()
	return res.Data.VideoData, nil
}

var webRouterJSONPattern = regexp.MustCompile(`window\._ROUTER_DATA\s*=\s*(\{.*?\})\s*;?\s*</script>`)

// FetchDetailWebFallback attempts to parse public hongguoduanju.com/detail?series_id=... if app detail fails.
func (c *HongguoClient) FetchDetailWebFallback(ctx context.Context, seriesID string) (*AppDetailVideoData, error) {
	body, err := c.http.FetchWebHTML(ctx, "https://hongguoduanju.com/detail?series_id="+url.QueryEscape(seriesID), map[string]string{
		"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
		"Referer":    "https://hongguoduanju.com/",
	}, 8*time.Second)
	if err != nil {
		return nil, err
	}
	m := webRouterJSONPattern.FindStringSubmatch(body)
	if len(m) < 2 {
		return nil, &UpstreamError{Code: "MISSING_EPISODE", Message: "web detail missing router data"}
	}
	quoted := QuoteLongJSONIDs(m[1])
	var router struct {
		LoaderData map[string]json.RawMessage `json:"loaderData"`
	}
	if err := json.Unmarshal([]byte(quoted), &router); err != nil {
		return nil, err
	}
	for _, raw := range router.LoaderData {
		var ld struct {
			SeriesDetail *struct {
				SeriesName           string           `json:"series_name"`
				SeriesTitle          string           `json:"series_title"`
				Title                string           `json:"title"`
				SeriesCover          string           `json:"series_cover"`
				Cover                string           `json:"cover"`
				SeriesIntro          string           `json:"series_intro"`
				Intro                string           `json:"intro"`
				EpisodeCnt           resolver.FlexInt `json:"episode_cnt"`
				EpisodeTotalCnt      resolver.FlexInt `json:"episode_total_cnt"`
				AccessibleEpisodeCnt resolver.FlexInt `json:"accessible_episode_cnt"`
				VIDList              []string         `json:"vid_list"`
			} `json:"seriesDetail"`
			VideoDetail *struct {
				SeriesName           string           `json:"series_name"`
				SeriesTitle          string           `json:"series_title"`
				Title                string           `json:"title"`
				SeriesCover          string           `json:"series_cover"`
				Cover                string           `json:"cover"`
				SeriesIntro          string           `json:"series_intro"`
				Intro                string           `json:"intro"`
				EpisodeCnt           resolver.FlexInt `json:"episode_cnt"`
				AccessibleEpisodeCnt resolver.FlexInt `json:"accessible_episode_cnt"`
				VIDList              []string         `json:"vid_list"`
			} `json:"videoDetail"`
		}
		if json.Unmarshal(raw, &ld) != nil {
			continue
		}
		if ld.SeriesDetail != nil && len(ld.SeriesDetail.VIDList) > 0 {
			d := ld.SeriesDetail
			out := &AppDetailVideoData{
				SeriesID:           seriesID,
				SeriesTitle:        firstStr(d.SeriesTitle, d.SeriesName, d.Title),
				SeriesCover:        firstStr(d.SeriesCover, d.Cover),
				SeriesIntro:        firstStr(d.SeriesIntro, d.Intro),
				EpisodeCnt:         d.EpisodeCnt,
				AccessibleEpisodes: d.AccessibleEpisodeCnt,
			}
			for _, v := range d.VIDList {
				out.VideoList = append(out.VideoList, AppEpisodeEntry{VID: v, VideoID: v})
			}
			return out, nil
		}
		if ld.VideoDetail != nil && len(ld.VideoDetail.VIDList) > 0 {
			d := ld.VideoDetail
			out := &AppDetailVideoData{
				SeriesID:           seriesID,
				SeriesTitle:        firstStr(d.SeriesTitle, d.SeriesName, d.Title),
				SeriesCover:        firstStr(d.SeriesCover, d.Cover),
				SeriesIntro:        firstStr(d.SeriesIntro, d.Intro),
				EpisodeCnt:         d.EpisodeCnt,
				AccessibleEpisodes: d.AccessibleEpisodeCnt,
			}
			for _, v := range d.VIDList {
				out.VideoList = append(out.VideoList, AppEpisodeEntry{VID: v, VideoID: v})
			}
			return out, nil
		}
	}
	return nil, &UpstreamError{Code: "MISSING_EPISODE", Message: "web detail had no episodes"}
}

// FetchDiscoveryRaw implements fetchAppDiscoveryRaw (REF L1166).
func (c *HongguoClient) FetchDiscoveryRaw(ctx context.Context, prof *signer.Profile, dev signer.Device, path string, params []signer.QueryKV, deadline time.Time) (json.RawMessage, error) {
	if _, ok := allowedDiscoveryPaths[path]; !ok {
		return nil, &UpstreamError{Code: "SECURITY_BLOCKED", Message: "disallowed discovery path: " + path}
	}
	signed, err := signer.BuildSignedRequest(prof, nil, "https://reading.snssdk.com"+path, params, dev, time.Now())
	if err != nil {
		return nil, err
	}
	signed.Headers["lc"] = "101"

	text, err := c.http.DoText(ctx, "GET", signed.URL, signed.Headers, nil, deadline)
	if err != nil {
		c.recordFailure()
		return nil, err
	}

	var envelope struct {
		Code       json.RawMessage `json:"code"`
		StatusCode json.RawMessage `json:"status_code"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		return nil, &UpstreamError{Code: "INVALID_RESPONSE", Message: "discovery JSON invalid", Err: err}
	}
	if parseJSONCode(envelope.Code, envelope.StatusCode) != 0 {
		return nil, &UpstreamError{Code: "DISCOVERY_REJECTED", Message: "discovery business rejected"}
	}
	c.recordSuccess()
	return json.RawMessage(text), nil
}

// FetchSearchRaw implements co() (REF L1120).
func (c *HongguoClient) FetchSearchRaw(ctx context.Context, prof *signer.Profile, dev signer.Device, params []signer.QueryKV, deadline time.Time) (json.RawMessage, error) {
	signed, err := signer.BuildSignedRequest(prof, nil, "https://reading.snssdk.com/reading/bookapi/search/tab/v", params, dev, time.Now())
	if err != nil {
		return nil, err
	}
	signed.Headers["lc"] = "101"

	text, err := c.http.DoText(ctx, "GET", signed.URL, signed.Headers, nil, deadline)
	if err != nil {
		c.recordFailure()
		return nil, err
	}

	var env struct {
		Code       json.RawMessage `json:"code"`
		SearchTabs json.RawMessage `json:"search_tabs"`
	}
	if err := json.Unmarshal([]byte(text), &env); err != nil {
		return nil, &UpstreamError{Code: "INVALID_RESPONSE", Message: "search JSON invalid", Err: err}
	}
	if parseJSONCode(env.Code) != 0 || len(env.SearchTabs) == 0 || string(env.SearchTabs) == "null" {
		return nil, &UpstreamError{Code: "BUSINESS_REJECTED", Message: "search rejected"}
	}
	c.recordSuccess()
	return json.RawMessage(text), nil
}

// FetchDanmakuRaw implements so() (REF L1120).
func (c *HongguoClient) FetchDanmakuRaw(ctx context.Context, prof *signer.Profile, dev signer.Device, seriesID, videoID string, startMs, durationMs int64, cursor string, deadline time.Time) (json.RawMessage, error) {
	payload := map[string]any{
		"comment_source": 601,
		"server_channel": 1000,
		"group_id":       videoID,
		"group_type":     30,
		"comment_type":   20,
		"sort":           1,
		"business_param": map[string]any{
			"book_id":               seriesID,
			"start_offset_time":     startMs,
			"playlet_item_duration": durationMs,
		},
		"count":             90,
		"cursor":            cursor,
		"aid":               8662,
		"compliance_status": 0,
	}
	bodyBytes, _ := json.Marshal(payload)
	endpoint := "https://reading.snssdk.com/novel/commentapi/comment/list/" + videoID + "/v1/"
	signed, err := signer.BuildSignedRequest(prof, bodyBytes, endpoint, nil, dev, time.Now())
	if err != nil {
		return nil, err
	}
	signed.Headers["lc"] = "101"

	text, err := c.http.DoText(ctx, "POST", signed.URL, signed.Headers, signed.Body, deadline)
	if err != nil {
		c.recordFailure()
		return nil, err
	}
	c.recordSuccess()
	return json.RawMessage(text), nil
}

// HeadMedia delegates HEAD probe to the underlying HTTP client.
func (c *HongguoClient) HeadMedia(ctx context.Context, mediaURL string, headers map[string]string, timeout time.Duration) (int, string, int64, error) {
	return c.http.HeadMedia(ctx, mediaURL, headers, timeout)
}

func firstStr(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
