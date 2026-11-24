package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/cache"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/resolver"
	"mioreel-gateway/server/internal/signer"
	"mioreel-gateway/server/internal/upstream"
)

type cachedVideoModel struct {
	model     *resolver.VideoModel
	routeHost string
	expiresAt int64
}

// ResolveService orchestrates device session -> video_model -> stream extraction -> HEAD probe -> caching.
type ResolveService struct {
	profiles       *profile.Manager
	devices        *device.Manager
	upstream       *upstream.HongguoClient
	series         *SeriesService
	media          *MediaService
	logger         *slog.Logger
	metrics        *observability.Metrics
	defaultTimeout int

	resolveCache *cache.TTLCache[gateway.ResolveData]
	modelCache   *cache.TTLCache[cachedVideoModel]
}

func NewResolveService(
	profiles *profile.Manager,
	devices *device.Manager,
	up *upstream.HongguoClient,
	series *SeriesService,
	media *MediaService,
	logger *slog.Logger,
	metrics *observability.Metrics,
	defaultTimeoutMs int,
) *ResolveService {
	if defaultTimeoutMs <= 0 {
		defaultTimeoutMs = 15000
	}
	return &ResolveService{
		profiles:       profiles,
		devices:        devices,
		upstream:       up,
		series:         series,
		media:          media,
		logger:         logger,
		metrics:        metrics,
		defaultTimeout: defaultTimeoutMs,
		resolveCache:   cache.NewTTLCache[gateway.ResolveData](1024),
		modelCache:     cache.NewTTLCache[cachedVideoModel](8), // docs/01-architecture.md §6.2: max 8 LRU
	}
}

func (s *ResolveService) Resolve(ctx context.Context, requestID string, req gateway.ResolveRequest) (gateway.ResolveData, error) {
	start := time.Now()
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "hongguo"
	}
	if source != "hongguo" {
		err := NewBadRequest("不支持的 source: " + source)
		s.logAndObserve(requestID, source, req.SeriesID, req.EpisodeNo, "", 0, 0, 0, "", false, start, err)
		return gateway.ResolveData{}, err
	}

	seriesID := strings.TrimSpace(req.SeriesID)
	vid := strings.TrimSpace(req.VID)
	if vid != "" && !isValidID(vid) {
		err := NewBadRequest("vid 必须为 10~30 位数字")
		s.logAndObserve(requestID, source, seriesID, req.EpisodeNo, "", 0, 0, 0, "", false, start, err)
		return gateway.ResolveData{}, err
	}
	if seriesID != "" && !isValidID(seriesID) {
		err := NewBadRequest("series_id 必须为 10~30 位数字")
		s.logAndObserve(requestID, source, seriesID, req.EpisodeNo, "", 0, 0, 0, "", false, start, err)
		return gateway.ResolveData{}, err
	}
	if vid == "" && (seriesID == "" || req.EpisodeNo < 1) {
		err := NewBadRequest("必须提供 vid，或同时提供合法的 series_id 与 episode_no (>=1)")
		s.logAndObserve(requestID, source, seriesID, req.EpisodeNo, "", 0, 0, 0, "", false, start, err)
		return gateway.ResolveData{}, err
	}

	preferCodec := normalizePreferCodec(req.PreferCodec)
	timeoutMs := req.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = s.defaultTimeout
	}
	if timeoutMs < 1000 {
		timeoutMs = 1000
	} else if timeoutMs > 60000 {
		timeoutMs = 60000
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)

	var seriesTitle, episodeTitle string
	episodeNo := req.EpisodeNo

	if vid == "" {
		ser, err := s.series.GetSeries(ctx, source, seriesID)
		if err != nil {
			ge := MapError(err)
			s.logAndObserve(requestID, source, seriesID, episodeNo, "", 0, 0, 0, "", false, start, ge)
			return gateway.ResolveData{}, ge
		}
		seriesTitle = ser.Title
		if episodeNo > len(ser.Episodes) {
			ge := NewNotFound("剧集或分集不存在 (upstream=MISSING_EPISODE)", "MISSING_EPISODE")
			s.logAndObserve(requestID, source, seriesID, episodeNo, "", 0, 0, 0, "", false, start, ge)
			return gateway.ResolveData{}, ge
		}
		ep := ser.Episodes[episodeNo-1]
		vid = ep.VID
		episodeTitle = ep.Title
	} else if episodeTitle == "" && episodeNo > 0 {
		episodeTitle = fmt.Sprintf("第%d集", episodeNo)
	}

	cacheKey := makeResolveCacheKey(source, vid, req.Quality, preferCodec)
	if req.AvoidURL == "" {
		if cached, ok := s.resolveCache.Get(cacheKey); ok {
			nowMs := time.Now().UnixMilli()
			if cached.ExpiresAt > nowMs+5000 {
				cached.ExpiresInMs = cached.ExpiresAt - nowMs
				if seriesID != "" && cached.SeriesID == "" {
					cached.SeriesID = seriesID
				}
				if seriesTitle != "" && cached.SeriesTitle == "" {
					cached.SeriesTitle = seriesTitle
				}
				if episodeNo > 0 && cached.EpisodeNo == 0 {
					cached.EpisodeNo = episodeNo
				}
				if episodeTitle != "" && cached.EpisodeTitle == "" {
					cached.EpisodeTitle = episodeTitle
				}
				if cached.DanmakuID == "" && cached.SeriesID != "" {
					cached.DanmakuID = fmt.Sprintf("%s:%s:%s", source, cached.SeriesID, vid)
				}
				if s.media.Enabled() {
					cached.MediaTicket = s.media.IssueTicket(vid, cached.PlayURL, cached.DecryptionKey, cached.PlayHeaders["User-Agent"], cached.ExpiresAt)
				}
				s.logAndObserve(requestID, source, cached.SeriesID, cached.EpisodeNo, "cache", 0, len(cached.Variants), cached.Quality, cached.Codec, true, start, nil)
				return cached, nil
			}
			s.resolveCache.Delete(cacheKey)
		}
	}

	dev, err := s.devices.Get(ctx, deadline)
	if err != nil {
		ge := MapError(err)
		s.logAndObserve(requestID, source, seriesID, episodeNo, "", 0, 0, 0, "", false, start, ge)
		return gateway.ResolveData{}, ge
	}

	modelKey := fmt.Sprintf("%s|%s|%s|%s", dev.IID, dev.DeviceID, dev.CDID, vid)
	var lastErr error
	var routeHost string
	var upstreamMs int64
	mediaFailure := "MEDIA_REJECTED"

	for attempt := 0; attempt < 2 && ctx.Err() == nil && time.Now().Before(deadline); attempt++ {
		refreshModel := attempt > 0 || req.AvoidURL != ""
		includeFallback := attempt > 0

		var model *resolver.VideoModel
		reusable := false
		if !refreshModel {
			if cm, ok := s.modelCache.Get(modelKey); ok && cm.expiresAt > time.Now().UnixMilli()+5000 {
				model = cm.model
				routeHost = cm.routeHost
				reusable = true
			}
		}

		if model == nil {
			upStart := time.Now()
			var fetchErr error
			model, routeHost, fetchErr = s.upstream.FetchVideoModel(ctx, s.profiles.Current(), dev, vid, deadline)
			upstreamMs += time.Since(upStart).Milliseconds()
			if fetchErr != nil {
				lastErr = fetchErr
				var ue *upstream.UpstreamError
				if errors.As(fetchErr, &ue) {
					if ue.RoutesExhausted || ue.Code == "BUSINESS_REJECTED" || ue.Code == "SECURITY_BLOCKED" ||
						ue.Code == "MISSING_MODEL" || ue.Code == "EMPTY_RESPONSE" || ue.Code == "RESPONSE_NOT_UTF8" ||
						ue.Code == "HTTP_403" || ue.Code == "HTTP_429" {
						break
					}
				}
				continue
			}
		}

		streams, extErr := resolver.Extract(model, vid, req.Quality)
		if (len(streams) == 0 || includeFallback || len(model.VideoList) == 0 || len(model.DynamicVideo) > 0) && len(model.FallbackAPI) > 0 {
			if fbModel, fbErr := s.upstream.FetchFallbackVideoModel(ctx, model.FallbackAPI, vid, deadline); fbErr == nil && fbModel != nil {
				if fbStreams, fbExtErr := resolver.Extract(fbModel, vid, req.Quality); fbExtErr == nil && len(fbStreams) > 0 {
					streams = mergeStreams(streams, fbStreams)
					extErr = nil
				}
			}
		}

		if extErr != nil && len(streams) == 0 {
			lastErr = extErr
			continue
		}

		if !reusable && len(streams) > 0 {
			exp := resolver.AppModelExpiry(model, 0)
			nowMs := time.Now().UnixMilli()
			if exp == 0 {
				exp = nowMs + 30_000
			}
			if exp > nowMs+300_000 {
				exp = nowMs + 300_000
			}
			if exp > nowMs+5000 {
				s.modelCache.Set(modelKey, cachedVideoModel{
					model:     model,
					routeHost: routeHost,
					expiresAt: exp,
				}, time.Duration(exp-nowMs)*time.Millisecond)
			}
		}

		ordered := sortStreamsForClient(streams, req.Quality, preferCodec, req.AvoidURL)
		candidate, sizeBytes, probeFailure := s.probeCandidates(ctx, ordered, deadline)
		if candidate == nil {
			if probeFailure != "" {
				mediaFailure = probeFailure
			}
			lastErr = &upstream.UpstreamError{Code: mediaFailure, Message: "candidate HEAD probe failed"}
			continue
		}

		nowMs := time.Now().UnixMilli()
		expMs := candidate.ExpiresAt
		if expMs <= 0 {
			expMs = nowMs + 30_000
		}
		if expMs > nowMs+300_000 {
			expMs = nowMs + 300_000
		}
		expInMs := expMs - nowMs
		if expInMs < 0 {
			expInMs = 0
		}

		if sizeBytes <= 0 {
			sizeBytes = candidate.MediaBytes
		}
		encryption := "none"
		if candidate.DecryptionKey != "" {
			encryption = "aes-128-cbc"
		}

		variants := make([]gateway.Variant, 0, len(ordered))
		// Ensure selected candidate is first item per docs/02-client-api.md §6.3
		variants = append(variants, gateway.Variant{
			URL:           candidate.URL,
			Quality:       candidate.Quality,
			Width:         candidate.Width,
			Height:        candidate.Height,
			Codec:         candidate.Codec,
			DecryptionKey: candidate.DecryptionKey,
			ExpiresAt:     expMs,
		})
		for _, v := range ordered {
			if v.URL == candidate.URL && v.DecryptionKey == candidate.DecryptionKey {
				continue
			}
			vExp := v.ExpiresAt
			if vExp <= 0 {
				vExp = expMs
			}
			if vExp > nowMs+300_000 {
				vExp = nowMs + 300_000
			}
			variants = append(variants, gateway.Variant{
				URL:           v.URL,
				Quality:       v.Quality,
				Width:         v.Width,
				Height:        v.Height,
				Codec:         v.Codec,
				DecryptionKey: v.DecryptionKey,
				ExpiresAt:     vExp,
			})
		}

		danmakuID := ""
		if seriesID != "" {
			danmakuID = fmt.Sprintf("%s:%s:%s", source, seriesID, vid)
		}
		playHeaders := map[string]string{
			"User-Agent": signer.AppUserAgent,
		}

		var ticket string
		if s.media.Enabled() {
			ticket = s.media.IssueTicket(vid, candidate.URL, candidate.DecryptionKey, signer.AppUserAgent, expMs)
		}

		data := gateway.ResolveData{
			Source:        source,
			SeriesID:      seriesID,
			SeriesTitle:   seriesTitle,
			EpisodeNo:     episodeNo,
			EpisodeID:     vid,
			EpisodeTitle:  episodeTitle,
			PlayURL:       candidate.URL,
			PlayHeaders:   playHeaders,
			DecryptionKey: candidate.DecryptionKey,
			Encryption:    encryption,
			MediaType:     "video/mp4",
			SizeBytes:     sizeBytes,
			Quality:       candidate.Quality,
			Width:         candidate.Width,
			Height:        candidate.Height,
			Codec:         candidate.Codec,
			DurationMs:    int64(model.VideoDuration * 1000),
			ExpiresAt:     expMs,
			ExpiresInMs:   expInMs,
			Variants:      variants,
			DanmakuID:     danmakuID,
			MediaTicket:   ticket,
		}

		cacheTTL := time.Duration(expMs-nowMs-30_000) * time.Millisecond
		if cacheTTL > 0 && req.AvoidURL == "" {
			s.resolveCache.Set(cacheKey, data, cacheTTL)
		}

		s.logAndObserve(requestID, source, seriesID, episodeNo, routeHost, upstreamMs, len(variants), candidate.Quality, candidate.Codec, false, start, nil)
		return data, nil
	}

	if lastErr == nil {
		if ctx.Err() != nil {
			lastErr = ctx.Err()
		} else {
			lastErr = &upstream.UpstreamError{Code: mediaFailure, Message: "no playable stream"}
		}
	}
	ge := MapError(lastErr)
	s.logAndObserve(requestID, source, seriesID, episodeNo, routeHost, upstreamMs, 0, 0, "", false, start, ge)
	return gateway.ResolveData{}, ge
}

func (s *ResolveService) probeCandidates(ctx context.Context, ordered []resolver.ExtractedStream, deadline time.Time) (*resolver.ExtractedStream, int64, string) {
	lastFailure := "MEDIA_REJECTED"
	for idx := 0; idx < len(ordered); {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			break
		}
		first := ordered[idx]
		group := []resolver.ExtractedStream{first}
		idx++
		if idx < len(ordered) && ordered[idx].Quality == first.Quality && ordered[idx].Codec == first.Codec {
			group = append(group, ordered[idx])
			idx++
		}

		type probeResult struct {
			stream    resolver.ExtractedStream
			ok        bool
			sizeBytes int64
			failCode  string
		}

		ch := make(chan probeResult, len(group))
		var wg sync.WaitGroup
		for _, cand := range group {
			cand := cand
			wg.Add(1)
			go func() {
				defer wg.Done()
				nowMs := time.Now().UnixMilli()
				if cand.ExpiresAt > 0 && cand.ExpiresAt <= nowMs+5000 {
					ch <- probeResult{failCode: "MEDIA_URL_EXPIRED"}
					return
				}
				rem := time.Until(deadline)
				if rem <= 0 {
					ch <- probeResult{failCode: "ETIMEDOUT"}
					return
				}
				if rem > 5*time.Second {
					rem = 5 * time.Second
				}
				// Strictly omit Referer on media CDN HEAD probe
				status, ct, cl, err := s.upstream.HeadMedia(ctx, cand.URL, map[string]string{
					"User-Agent": signer.AppUserAgent,
				}, rem)
				if err != nil {
					var ue *upstream.UpstreamError
					if errors.As(err, &ue) {
						ch <- probeResult{failCode: ue.Code}
					} else {
						ch <- probeResult{failCode: "MEDIA_NETWORK"}
					}
					return
				}
				if status != 200 && status != 206 {
					ch <- probeResult{failCode: "MEDIA_HTTP_" + strconv.Itoa(status)}
					return
				}
				if !strings.HasPrefix(strings.ToLower(ct), "video/") {
					ch <- probeResult{failCode: "MEDIA_TYPE_INVALID"}
					return
				}
				ch <- probeResult{stream: cand, ok: true, sizeBytes: cl}
			}()
		}
		go func() {
			wg.Wait()
			close(ch)
		}()

		for res := range ch {
			if res.ok {
				chosen := res.stream
				return &chosen, res.sizeBytes, ""
			}
			if res.failCode != "" {
				lastFailure = res.failCode
			}
		}
	}
	return nil, 0, lastFailure
}

func sortStreamsForClient(streams []resolver.ExtractedStream, requestedQuality int, preferCodec []string, avoidURL string) []resolver.ExtractedStream {
	preferred := requestedQuality
	if preferred <= 0 {
		preferred = 1080
	}
	preferLower := requestedQuality > 0 && requestedQuality < 1080

	qualityRank := func(q int) (int, int) {
		if q <= 0 {
			return 3, 0
		}
		if q == preferred {
			return 0, 0
		}
		preferredSide := q > preferred
		if preferLower {
			preferredSide = q < preferred
		}
		tier := 2
		if preferredSide {
			tier = 1
		}
		diff := q - preferred
		if diff < 0 {
			diff = -diff
		}
		return tier, diff
	}

	codecPos := func(c string) int {
		for i, p := range preferCodec {
			if c == p {
				return i
			}
		}
		return len(preferCodec) + 1
	}

	out := append([]resolver.ExtractedStream(nil), streams...)
	sort.SliceStable(out, func(i, j int) bool {
		avoidI := avoidURL != "" && out[i].URL == avoidURL
		avoidJ := avoidURL != "" && out[j].URL == avoidURL
		if avoidI != avoidJ {
			return !avoidI
		}
		ti, di := qualityRank(out[i].Quality)
		tj, dj := qualityRank(out[j].Quality)
		if ti != tj {
			return ti < tj
		}
		if di != dj {
			return di < dj
		}
		ci := codecPos(out[i].Codec)
		cj := codecPos(out[j].Codec)
		if ci != cj {
			return ci < cj
		}
		return out[i].CodecRank < out[j].CodecRank
	})
	return out
}

func mergeStreams(a, b []resolver.ExtractedStream) []resolver.ExtractedStream {
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]resolver.ExtractedStream, 0, len(a)+len(b))
	for _, s := range append(append([]resolver.ExtractedStream(nil), a...), b...) {
		k := s.URL + ":" + s.DecryptionKey
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, s)
	}
	return out
}

func normalizePreferCodec(in []string) []string {
	if len(in) == 0 {
		return []string{"h264", "hevc", "mp4"}
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.ToLower(strings.TrimSpace(c))
		switch c {
		case "avc", "avc1":
			c = "h264"
		case "h265", "hvc1", "hev1", "bytevc1":
			c = "hevc"
		}
		if c != "" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return []string{"h264", "hevc", "mp4"}
	}
	return out
}

func makeResolveCacheKey(source, vid string, quality int, preferCodec []string) string {
	raw := source + "|" + vid + "|" + strconv.Itoa(quality) + "|" + strings.Join(preferCodec, ",")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func (s *ResolveService) logAndObserve(
	requestID, source, seriesID string,
	episodeNo int,
	route string,
	upstreamMs int64,
	variants int,
	selectedQuality int,
	selectedCodec string,
	cached bool,
	start time.Time,
	err error,
) {
	elapsedMs := time.Since(start).Milliseconds()
	code := 0
	errMsg := ""
	if err != nil {
		if ge, ok := err.(*GatewayError); ok {
			code = ge.Code
			errMsg = ge.Message
		} else {
			code = 50001
			errMsg = err.Error()
		}
	}

	if s.metrics != nil {
		s.metrics.ObserveResolve(code, float64(elapsedMs), cached)
	}
	if s.logger != nil {
		lvl := slog.LevelInfo
		if code != 0 {
			lvl = slog.LevelWarn
		}
		s.logger.Log(context.Background(), lvl, "resolve",
			slog.String("event", "resolve"),
			slog.String("request_id", requestID),
			slog.String("source", source),
			slog.String("series_id", seriesID),
			slog.Int("episode_no", episodeNo),
			slog.String("upstream_route", route),
			slog.Int64("upstream_ms", upstreamMs),
			slog.Int("variants", variants),
			slog.Int("selected_quality", selectedQuality),
			slog.String("selected_codec", selectedCodec),
			slog.Bool("cached", cached),
			slog.Int64("elapsed_ms", elapsedMs),
			slog.Int("code", code),
			slog.String("error", errMsg),
		)
	}
}
