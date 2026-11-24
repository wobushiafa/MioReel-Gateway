package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/device"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/profile"
	"mioreel-gateway/server/internal/service"
	"mioreel-gateway/server/internal/upstream"
)

// ServerDeps groups dependencies needed to assemble the gateway HTTP handler.
type ServerDeps struct {
	Config   config.Config
	Logger   *slog.Logger
	Metrics  *observability.Metrics
	Profiles *profile.Manager
	Devices  *device.Manager
	Upstream  *upstream.HongguoClient
	Catalog   *service.CatalogService
	Discovery *service.DiscoveryService
	Series    *service.SeriesService
	Resolve  *service.ResolveService
	Search   *service.SearchService
	Danmaku  *service.DanmakuService
	Media    *service.MediaService
}

// NewHandler constructs the complete HTTP router and middleware stack.
func NewHandler(deps ServerDeps) http.Handler {
	h := &Handlers{
		version:   deps.Config.Version,
		startedAt: time.Now(),
		profiles:  deps.Profiles,
		devices:   deps.Devices,
		upstream:  deps.Upstream,
		metrics:   deps.Metrics,
		catalog:   deps.Catalog,
		discovery: deps.Discovery,
		series:    deps.Series,
		resolve:   deps.Resolve,
		search:    deps.Search,
		danmaku:   deps.Danmaku,
		media:     deps.Media,
	}

	allowlist := parseIPAllowlist(deps.Config.IPAllowlist)
	auth := newAuthenticator(deps.Config)
	limiter := newRateLimiter(deps.Config.TokenQPS, deps.Config.TokenConcurrency, deps.Config.IPQPS)

	protect := func(fn http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())
			ip := clientIP(r, deps.Config.TrustProxyHeaders)
			if !allowlist.allows(ip) {
				writeError(w, reqID, service.NewForbidden("IP 不在允许列表中"))
				return
			}
			tokenID, authErr := auth.authenticate(r)
			if authErr != nil {
				writeError(w, reqID, authErr)
				return
			}
			release, retryAfter, ok := limiter.acquire(tokenID, ip)
			if !ok {
				writeError(w, reqID, service.NewRateLimited(retryAfter))
				return
			}
			defer release()

			ctx := context.WithValue(r.Context(), ctxKeyTokenID, tokenID)
			fn(w, r.WithContext(ctx))
		})
	}

	mux := http.NewServeMux()
	// Diagnostic endpoints
	mux.HandleFunc("GET /api/v1/health", h.Health)
	mux.HandleFunc("GET /metrics", h.Metrics)

	// Authenticated business endpoints
	mux.Handle("GET /api/v1/catalog", protect(h.Catalog))
	mux.Handle("GET /api/v1/rankings/boards", protect(h.RankingBoards))
	mux.Handle("GET /api/v1/rankings", protect(h.Rankings))
	mux.Handle("GET /api/v1/recommendations", protect(h.Recommendations))
	mux.Handle("GET /api/v1/feed", protect(h.Feed))
	mux.Handle("POST /api/v1/series", protect(h.Series))
	mux.Handle("POST /api/v1/resolve", protect(h.Resolve))
	mux.Handle("GET /api/v1/search", protect(h.Search))
	mux.Handle("GET /api/v1/danmaku", protect(h.Danmaku))

	// Media proxy authenticates via HMAC-signed ticket in URL
	mux.HandleFunc("GET /api/v1/media/{ticket}", h.Media)

	return withRequestID(withRecovery(deps.Logger, mux))
}
