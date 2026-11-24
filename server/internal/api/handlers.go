package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/service"
	"mioreel-gateway/server/internal/upstream"
)

type Handlers struct {
	version   string
	startedAt time.Time
	profiles  *profile.Manager
	devices   *device.Manager
	upstream  *upstream.HongguoClient
	metrics   *observability.Metrics

	catalog   *service.CatalogService
	discovery *service.DiscoveryService
	series    *service.SeriesService
	resolve *service.ResolveService
	search  *service.SearchService
	danmaku *service.DanmakuService
	media   *service.MediaService
}

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	profID, sdkVer, rotatedAt, profDegraded := h.profiles.Status()
	lastSuccess, consecutiveFails := h.upstream.HealthStats()
	devReady := h.devices.Ready()

	status := "ok"
	if profDegraded || consecutiveFails >= 3 {
		status = "degraded"
	}
	if consecutiveFails >= 10 && !devReady {
		status = "down"
	}

	lastSuccessStr := ""
	if !lastSuccess.IsZero() {
		lastSuccessStr = lastSuccess.UTC().Format(time.RFC3339)
	}

	writeJSON(w, http.StatusOK, gateway.HealthResponse{
		Status:   status,
		Version:  h.version,
		UptimeMs: time.Since(h.startedAt).Milliseconds(),
		Signing: gateway.SigningHealth{
			ProfileID:  profID,
			SDKVersion: sdkVer,
			RotatedAt:  rotatedAt.UTC().Format(time.RFC3339),
		},
		Upstream: gateway.UpstreamHealth{
			DeviceReady:         devReady,
			LastSuccessAt:       lastSuccessStr,
			ConsecutiveFailures: consecutiveFails,
		},
	})
}

func parsePageAndLimit(r *http.Request, defaultLimit int) (int, int, error) {
	q := r.URL.Query()
	page := 1
	if pStr := strings.TrimSpace(q.Get("page")); pStr != "" {
		p, err := strconv.Atoi(pStr)
		if err != nil || p < 1 {
			return 0, 0, service.NewBadRequest("page 参数非法")
		}
		page = p
	}
	limit := defaultLimit
	if lStr := strings.TrimSpace(q.Get("limit")); lStr != "" {
		l, err := strconv.Atoi(lStr)
		if err != nil || l < 1 {
			return 0, 0, service.NewBadRequest("limit 参数非法")
		}
		limit = l
	}
	return page, limit, nil
}

func (h *Handlers) Catalog(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	q := r.URL.Query()
	category := q.Get("category")
	sortMode := strings.TrimSpace(q.Get("sort"))
	page, limit, err := parsePageAndLimit(r, 40)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}

	var data gateway.CatalogData
	switch sortMode {
	case "recommend":
		data, err = h.discovery.GetRecommendations(r.Context(), category, page, limit)
	case "hot":
		board := "hongguo-app:all:ranklist_hot_sc"
		switch strings.TrimSpace(category) {
		case "real-drama", "playlet":
			board = "hongguo-app:human:human_hot_sc"
		case "comic-drama", "comic", "motion_comic":
			board = "hongguo-app:comic_series_rank:comic_series_hot_sc"
		case "ai-drama", "ai_playlet":
			board = "hongguo-app:ai_playlet:ai_playlet_hot_sc"
		}
		data, err = h.discovery.GetRankings(r.Context(), board, page, limit)
	case "":
		data, err = h.catalog.GetCatalog(r.Context(), category, page, limit)
	default:
		writeError(w, reqID, service.NewBadRequest("sort 仅支持 recommend / hot"))
		return
	}
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.CatalogData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) RankingBoards(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	data, err := h.discovery.GetRankingBoards(r.Context())
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.RankingBoardsData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Rankings(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	board := r.URL.Query().Get("board")
	page, limit, err := parsePageAndLimit(r, 40)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	data, err := h.discovery.GetRankings(r.Context(), board, page, limit)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.CatalogData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Recommendations(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	genre := r.URL.Query().Get("genre")
	page, limit, err := parsePageAndLimit(r, 40)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	data, err := h.discovery.GetRecommendations(r.Context(), genre, page, limit)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.CatalogData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Feed(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	scope := r.URL.Query().Get("scope")
	page, limit, err := parsePageAndLimit(r, 40)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	data, err := h.discovery.GetFeed(r.Context(), scope, page, limit)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.FeedData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Series(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req gateway.SeriesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, reqID, service.NewBadRequest("JSON 请求体格式非法"))
		return
	}

	data, err := h.series.GetSeries(r.Context(), req.Source, req.SeriesID)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.SeriesData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Resolve(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req gateway.ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, reqID, service.NewBadRequest("JSON 请求体格式非法"))
		return
	}

	data, err := h.resolve.Resolve(r.Context(), reqID, req)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.ResolveData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	q := r.URL.Query()
	query := q.Get("q")
	page := 1
	if pStr := strings.TrimSpace(q.Get("page")); pStr != "" {
		p, err := strconv.Atoi(pStr)
		if err != nil || p < 1 {
			writeError(w, reqID, service.NewBadRequest("page 参数非法"))
			return
		}
		page = p
	}

	data, err := h.search.Search(r.Context(), query, page)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.SearchData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Danmaku(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	q := r.URL.Query()
	danmakuID := q.Get("danmaku_id")
	var startMs, durationMs int64
	if s := strings.TrimSpace(q.Get("start_ms")); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v < 0 {
			writeError(w, reqID, service.NewBadRequest("start_ms 参数非法"))
			return
		}
		startMs = v
	}
	if d := strings.TrimSpace(q.Get("duration_ms")); d != "" {
		v, err := strconv.ParseInt(d, 10, 64)
		if err != nil || v <= 0 {
			writeError(w, reqID, service.NewBadRequest("duration_ms 参数非法"))
			return
		}
		durationMs = v
	}

	data, err := h.danmaku.GetDanmaku(r.Context(), danmakuID, startMs, durationMs)
	if err != nil {
		writeError(w, reqID, service.MapError(err))
		return
	}
	writeJSON(w, http.StatusOK, gateway.APIResponse[gateway.DanmakuData]{
		Code:      0,
		Message:   "ok",
		RequestID: reqID,
		Data:      data,
	})
}

func (h *Handlers) Media(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	ticket := r.PathValue("ticket")
	if err := h.media.ServeMedia(w, r, ticket); err != nil {
		ge := service.MapError(err)
		httpStatus := ge.HTTPStatus
		if httpStatus == http.StatusOK {
			httpStatus = http.StatusBadGateway
		}
		writeJSON(w, httpStatus, gateway.APIResponse[any]{
			Code:      ge.Code,
			Message:   ge.Message,
			RequestID: reqID,
			Data:      nil,
		})
	}
}

func (h *Handlers) Metrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	h.metrics.WritePrometheus(w)
}
