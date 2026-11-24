package api

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math/bits"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/service"
	"mioreel-gateway/server/internal/upstream"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func makeSpadeA(keyHex string) string {
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
	enc[0] = enc[1] ^ enc[2]
	return base64.StdEncoding.EncodeToString(enc)
}

func makeSpadeURL(plainURL string, keySeed []byte) string {
	salt, _ := hex.DecodeString("4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58")
	a := sha512.Sum512(keySeed)
	mix := append(a[:], salt...)
	c := sha512.Sum512(mix)

	padLen := 16 - (len(plainURL) % 16)
	padded := make([]byte, len(plainURL)+padLen)
	copy(padded, plainURL)
	for idx := len(plainURL); idx < len(padded); idx++ {
		padded[idx] = byte(padLen)
	}
	blk, _ := aes.NewCipher(c[0:16])
	ct := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blk, c[16:32]).CryptBlocks(ct, padded)
	return base64.StdEncoding.EncodeToString(append([]byte{168, 0, 1, 0}, ct...))
}

func newTestServer(t *testing.T, rt http.RoundTripper, cfgMod func(*config.Config)) http.Handler {
	t.Helper()
	cfg := config.Default()
	cfg.AuthTokens = []string{"test-secret-token"}
	cfg.DeviceStorePath = filepath.Join(t.TempDir(), "device.json")
	if cfgMod != nil {
		cfgMod(&cfg)
	}

	logger := slog.New(slog.DiscardHandler)
	metrics := observability.NewMetrics()
	profMgr := profile.NewManager(cfg, logger, metrics)
	upCli := upstream.NewClientWithTransport(rt)
	hgCli := upstream.NewHongguoClient(upCli, metrics)
	devMgr := device.NewManager(upCli, cfg.DeviceStorePath, logger)

	seriesSvc := service.NewSeriesService(profMgr, devMgr, hgCli)
	catalogSvc := service.NewCatalogService(profMgr, devMgr, hgCli)
	discoverySvc := service.NewDiscoveryService(profMgr, devMgr, hgCli)
	mediaSvc := service.NewMediaService(cfg.MediaProxyEnabled, cfg.MediaTicketSecret, cfg.MediaFFmpegPath, t.TempDir())
	resolveSvc := service.NewResolveService(profMgr, devMgr, hgCli, seriesSvc, mediaSvc, logger, metrics, cfg.DefaultResolveTimeoutMs)
	searchSvc := service.NewSearchService(profMgr, devMgr, hgCli)
	danmakuSvc := service.NewDanmakuService(profMgr, devMgr, hgCli)

	return NewHandler(ServerDeps{
		Config:   cfg,
		Logger:   logger,
		Metrics:  metrics,
		Profiles: profMgr,
		Devices:  devMgr,
		Upstream:  hgCli,
		Catalog:   catalogSvc,
		Discovery: discoverySvc,
		Series:    seriesSvc,
		Resolve:  resolveSvc,
		Search:   searchSvc,
		Danmaku:  danmakuSvc,
		Media:    mediaSvc,
	})
}

func TestEndToEndResolveWithRouteFallbackAndCaching(t *testing.T) {
	wantKey := "982735c47051044342ede4ef5d3e8cb5"
	seed := []byte("0123456789abcdef0123456789abcdef")
	cdnURL := "https://v5-reading-video.qznovelvod.com/9fd7ad/6afae9ff/video/tos/cn/clip.mp4?dy_q=1794807027"
	expSec := time.Now().Add(5 * time.Minute).Unix()

	var modelCalls atomic.Int32
	var headCalls atomic.Int32
	var regCalls atomic.Int32

	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Path, "/service/2/device_register/"):
			regCalls.Add(1)
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"install_id_str": "4069772115858074",
					"device_id_str": "4069772115853978"
				}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/novel/player/video_detail/v1/"):
			// Must include the 6 signature headers
			for _, h := range []string{"x-gorgon", "x-khronos", "x-ladon", "x-argus", "x-helios", "x-medusa", "x-ss-stub"} {
				if r.Header.Get(h) == "" {
					t.Fatalf("missing header %s on video_detail", h)
				}
			}
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code": 0,
					"data": {
						"video_data": {
							"series_id": "7701678491724418073",
							"series_title": "二婚嫁对人，前夫悔破防",
							"series_cover": "https://p3-novel.byteimg.com/novel-pic/abc~noop.image",
							"episode_cnt": 2,
							"video_list": [
								{"vid": "7701680675979545625", "title": "第1集"},
								{"vid": "7701680675979545626", "title": "第2集"}
							]
						}
					}
				}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/novel/player/multi_video_model/v1/") && r.URL.Host == "api5-normal-sinfonlineb.fqnovel.com":
			// Simulate primary route 502 so fallback route handles it
			modelCalls.Add(1)
			return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(""))}, nil
		case strings.Contains(r.URL.Path, "/novel/player/multi_video_model/v1/") && r.URL.Host == "reading.snssdk.com":
			modelCalls.Add(1)
			innerModel, _ := json.Marshal(map[string]any{
				"key_seed":    base64.StdEncoding.EncodeToString(seed),
				"expire_time": expSec,
				"video_list": map[string]any{
					"v1080": map[string]any{
						"codec_type": "hevc",
						"vwidth":     1080,
						"vheight":    1920,
						"size":       9131576,
						"spade_a":    makeSpadeA(wantKey),
						"main_url":   makeSpadeURL(cdnURL, seed),
					},
				},
			})
			outer, _ := json.Marshal(map[string]any{
				"code": 0,
				"data": map[string]any{
					"7701680675979545625": map[string]any{
						"expire_time": expSec,
						"video_model": string(innerModel),
					},
				},
			})
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(bytes.NewReader(outer)),
			}, nil
		case r.Method == http.MethodHead && r.URL.Host == "v5-reading-video.qznovelvod.com":
			headCalls.Add(1)
			// Verify Referer is strictly omitted
			if r.Header.Get("Referer") != "" {
				t.Fatalf("CDN HEAD request leaked Referer: %s", r.Header.Get("Referer"))
			}
			return &http.Response{
				StatusCode:    200,
				ContentLength: 9131576,
				Header:        http.Header{"Content-Type": []string{"video/mp4"}},
				Body:          io.NopCloser(strings.NewReader("")),
			}, nil
		default:
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.String())
			return nil, nil
		}
	})

	handler := newTestServer(t, rt, nil)

	// 1. Health check
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("health status=%d", rec.Code)
	}
	var health gateway.HealthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &health); err != nil {
		t.Fatalf("unmarshal health: %v", err)
	}
	if health.Status != "ok" || health.Signing.ProfileID != "646637794" {
		t.Fatalf("unexpected health: %+v", health)
	}

	// 2. First Resolve (cold)
	body := `{"series_id":"7701678491724418073","episode_no":1}`
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest("POST", "/api/v1/resolve", strings.NewReader(body))
	req1.Header.Set("Authorization", "Bearer test-secret-token")
	req1.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec1, req1)
	if rec1.Code != 200 {
		t.Fatalf("resolve HTTP status=%d", rec1.Code)
	}
	var env1 gateway.APIResponse[gateway.ResolveData]
	if err := json.Unmarshal(rec1.Body.Bytes(), &env1); err != nil {
		t.Fatalf("unmarshal resolve: %v", err)
	}
	if env1.Code != 0 {
		t.Fatalf("resolve failed: code=%d msg=%s", env1.Code, env1.Message)
	}
	if env1.Data.PlayURL != cdnURL || env1.Data.DecryptionKey != wantKey || env1.Data.Encryption != "aes-128-cbc" {
		t.Fatalf("unexpected resolve data: %+v", env1.Data)
	}
	if _, hasRef := env1.Data.PlayHeaders["Referer"]; hasRef {
		t.Fatal("play_headers must not contain Referer")
	}
	if env1.Data.DanmakuID != "hongguo:7701678491724418073:7701680675979545625" {
		t.Fatalf("unexpected danmaku_id: %s", env1.Data.DanmakuID)
	}

	callsAfterFirst := modelCalls.Load()
	if callsAfterFirst != 2 {
		t.Fatalf("expected 2 model calls (primary 502 + fallback 200), got %d", callsAfterFirst)
	}

	// 3. Second Resolve (cache hit -> 0 new upstream calls!)
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/resolve", strings.NewReader(`{"vid":"7701680675979545625"}`))
	req2.Header.Set("X-API-Key", "test-secret-token")
	handler.ServeHTTP(rec2, req2)
	var env2 gateway.APIResponse[gateway.ResolveData]
	_ = json.Unmarshal(rec2.Body.Bytes(), &env2)
	if env2.Code != 0 || env2.Data.PlayURL != cdnURL {
		t.Fatalf("cached resolve failed: %+v", env2)
	}
	if modelCalls.Load() != callsAfterFirst || headCalls.Load() != 1 || regCalls.Load() != 1 {
		t.Fatalf("expected resolve cache hit with no new upstream requests; model=%d head=%d reg=%d",
			modelCalls.Load(), headCalls.Load(), regCalls.Load())
	}
}

func TestAuthHMACRateLimitAndValidation(t *testing.T) {
	handler := newTestServer(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(""))}, nil
	}), func(c *config.Config) {
		c.HMACSecret = "hmac-top-secret"
		c.TokenQPS = 2
		c.IPQPS = 100
	})

	// Missing auth -> HTTP 401, code 40101
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/series", strings.NewReader(`{"series_id":"1234567890"}`)))
	if rec.Code != 401 {
		t.Fatalf("expected 401, got %d", rec.Code)
	}

	// Valid Bearer + HMAC signature -> reaches validation (40001 with HTTP 200)
	signHMAC := func(method, path, body, nonce string) (string, string) {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		bHash := sha256.Sum256([]byte(body))
		canon := method + "\n" + path + "\n" + ts + "\n" + nonce + "\n" + hex.EncodeToString(bHash[:])
		mac := hmac.New(sha256.New, []byte("hmac-top-secret"))
		mac.Write([]byte(canon))
		return ts, hex.EncodeToString(mac.Sum(nil))
	}

	badBody := `{"series_id":"bad"}`
	ts, sig := signHMAC("POST", "/api/v1/series", badBody, "nonce-1234567890abcdef")
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/series", strings.NewReader(badBody))
	req2.Header.Set("Authorization", "Bearer test-secret-token")
	req2.Header.Set("X-Timestamp", ts)
	req2.Header.Set("X-Nonce", "nonce-1234567890abcdef")
	req2.Header.Set("X-Signature", sig)
	handler.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Fatalf("expected HTTP 200 for 40001 validation error, got %d", rec2.Code)
	}
	var env gateway.APIResponse[any]
	_ = json.Unmarshal(rec2.Body.Bytes(), &env)
	if env.Code != 40001 {
		t.Fatalf("expected code 40001, got %d (%s)", env.Code, env.Message)
	}

	// Replay same nonce -> 401
	recReplay := httptest.NewRecorder()
	reqReplay := httptest.NewRequest("POST", "/api/v1/series", strings.NewReader(badBody))
	reqReplay.Header.Set("Authorization", "Bearer test-secret-token")
	reqReplay.Header.Set("X-Timestamp", ts)
	reqReplay.Header.Set("X-Nonce", "nonce-1234567890abcdef")
	reqReplay.Header.Set("X-Signature", sig)
	handler.ServeHTTP(recReplay, reqReplay)
	if recReplay.Code != 401 {
		t.Fatalf("expected 401 on replayed nonce, got %d", recReplay.Code)
	}

	// Burst rate limit -> 42901 + Retry-After
	got42901 := false
	for i := 0; i < 5; i++ {
		nonce := "nonce-burst-000000000" + strconv.Itoa(i)
		t2, s2 := signHMAC("POST", "/api/v1/series", badBody, nonce)
		r := httptest.NewRequest("POST", "/api/v1/series", strings.NewReader(badBody))
		r.Header.Set("Authorization", "Bearer test-secret-token")
		r.Header.Set("X-Timestamp", t2)
		r.Header.Set("X-Nonce", nonce)
		r.Header.Set("X-Signature", s2)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		var resp gateway.APIResponse[any]
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if resp.Code == 42901 && w.Header().Get("Retry-After") != "" {
			got42901 = true
			break
		}
	}
	if !got42901 {
		t.Fatal("expected rate limiter to trigger 42901 with Retry-After")
	}
}

func TestDiscoveryEndpointsAndCatalogSort(t *testing.T) {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		q := r.URL.Query()
		switch {
		case strings.Contains(r.URL.Path, "/service/2/device_register/"):
			return &http.Response{
				StatusCode: 200,
				Body:       io.NopCloser(strings.NewReader(`{"install_id_str":"4069772115858074","device_id_str":"4069772115853978"}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/reading/bookapi/plan/v"):
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code": 0,
					"data": [{
						"cell_id_str": "cell_rank_1",
						"next_offset": 20,
						"has_more": true,
						"cell_selector": {
							"outer_row": {
								"items": [{
									"selector_item_id": "all",
									"show_name": "全部",
									"is_selected": true,
									"sub_cell_selector": {"outer_row": {"items": [
										{"selector_item_id": "ranklist_hot_sc", "show_name": "热播榜", "is_selected": true}
									]}}
								}]
							}
						},
						"video_data": [{"series_id_str": "7701678491724418073", "title": "热播短剧", "episode_cnt": 50}]
					}]
				}`)),
			}, nil
		case strings.Contains(r.URL.Path, "/reading/bookapi/bookmall/tab/v"):
			tabType := q.Get("tab_type")
			if tabType == "16" {
				return &http.Response{
					StatusCode: 200,
					Body: io.NopCloser(strings.NewReader(`{
						"code": 0,
						"data": {"tab_item": [{
							"tab_type": 16,
							"next_offset": 10,
							"bottom_unlimited": true,
							"cell_data": [{"cell_id_str": "cell_feed", "video_data": [
								{"series_id_str": "7701678491724418082", "title": "推荐流剧集", "episode_cnt": 60}
							]}]
						}]}
					}`)),
				}, nil
			}
			return &http.Response{
				StatusCode: 200,
				Body: io.NopCloser(strings.NewReader(`{
					"code": 0,
					"data": {"tab_item": [{"tab_type": 38, "cell_data": [{
						"cell_id_str": "cell_rec_38",
						"next_offset": 20,
						"has_more": true,
						"video_data": [{"series_id_str": "7701678491724418080", "title": "分类型推荐剧集", "episode_cnt": 40}]
					}]}]}
				}`)),
			}, nil
		default:
			return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
	})

	handler := newTestServer(t, rt, func(c *config.Config) {
		c.TokenQPS = 100
		c.IPQPS = 100
	})

	doGet := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer test-secret-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	// 1. /api/v1/rankings/boards
	w := doGet("/api/v1/rankings/boards")
	var boardsResp gateway.APIResponse[gateway.RankingBoardsData]
	if err := json.Unmarshal(w.Body.Bytes(), &boardsResp); err != nil || boardsResp.Code != 0 || len(boardsResp.Data.Items) != 1 {
		t.Fatalf("rankings/boards failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	// 2. /api/v1/rankings
	w = doGet("/api/v1/rankings?board=hongguo-app:all:ranklist_hot_sc&page=1")
	var rankResp gateway.APIResponse[gateway.CatalogData]
	if err := json.Unmarshal(w.Body.Bytes(), &rankResp); err != nil || rankResp.Code != 0 || len(rankResp.Data.List) != 1 {
		t.Fatalf("rankings failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	// 3. /api/v1/recommendations
	w = doGet("/api/v1/recommendations?genre=real-drama&page=1")
	var recResp gateway.APIResponse[gateway.CatalogData]
	if err := json.Unmarshal(w.Body.Bytes(), &recResp); err != nil || recResp.Code != 0 || recResp.Data.List[0].Title != "分类型推荐剧集" {
		t.Fatalf("recommendations failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	// 4. /api/v1/feed
	w = doGet("/api/v1/feed?page=1")
	var feedResp gateway.APIResponse[gateway.FeedData]
	if err := json.Unmarshal(w.Body.Bytes(), &feedResp); err != nil || feedResp.Code != 0 || !feedResp.Data.HasMore || feedResp.Data.List[0].Title != "推荐流剧集" {
		t.Fatalf("feed failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	// 5. /api/v1/catalog?category=real-drama&sort=recommend
	w = doGet("/api/v1/catalog?category=real-drama&sort=recommend&page=1")
	var catRecResp gateway.APIResponse[gateway.CatalogData]
	if err := json.Unmarshal(w.Body.Bytes(), &catRecResp); err != nil || catRecResp.Code != 0 || catRecResp.Data.List[0].Title != "分类型推荐剧集" {
		t.Fatalf("catalog sort=recommend failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}

	// 6. /api/v1/catalog?sort=hot
	w = doGet("/api/v1/catalog?sort=hot&page=1")
	var catHotResp gateway.APIResponse[gateway.CatalogData]
	if err := json.Unmarshal(w.Body.Bytes(), &catHotResp); err != nil || catHotResp.Code != 0 || catHotResp.Data.List[0].Title != "热播短剧" {
		t.Fatalf("catalog sort=hot failed: code=%d body=%s err=%v", w.Code, w.Body.String(), err)
	}
}
