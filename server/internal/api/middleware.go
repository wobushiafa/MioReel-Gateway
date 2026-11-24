package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	gateway "mioreel-gateway/server"
	"mioreel-gateway/server/internal/cache"
	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/service"
)

type ctxKey string

const (
	ctxKeyRequestID ctxKey = "request_id"
	ctxKeyTokenID   ctxKey = "token_id"
)

func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKeyRequestID).(string); ok && v != "" {
		return v
	}
	return "unknown"
}

func newRequestID() string {
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, reqID string, ge *service.GatewayError) {
	if ge.RetryAfterSec > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(ge.RetryAfterSec))
	}
	writeJSON(w, ge.HTTPStatus, gateway.APIResponse[any]{
		Code:      ge.Code,
		Message:   ge.Message,
		RequestID: reqID,
		Data:      nil,
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Request-Id"))
		if id == "" || len(id) > 64 {
			id = newRequestID()
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), ctxKeyRequestID, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func withRecovery(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				reqID := RequestIDFromContext(r.Context())
				if logger != nil {
					logger.Error("panic recovered",
						slog.String("request_id", reqID),
						slog.Any("panic", rec),
						slog.String("stack", string(debug.Stack())),
					)
				}
				writeError(w, reqID, &service.GatewayError{
					Code:       50001,
					HTTPStatus: http.StatusOK,
					Message:    "服务端内部错误",
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type ipAllowlist struct {
	nets []*net.IPNet
	ips  []net.IP
}

func parseIPAllowlist(entries []string) *ipAllowlist {
	if len(entries) == 0 {
		return nil
	}
	al := &ipAllowlist{}
	for _, raw := range entries {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.Contains(raw, "/") {
			if _, cidr, err := net.ParseCIDR(raw); err == nil {
				al.nets = append(al.nets, cidr)
			}
		} else if ip := net.ParseIP(raw); ip != nil {
			al.ips = append(al.ips, ip)
		}
	}
	if len(al.nets) == 0 && len(al.ips) == 0 {
		return nil
	}
	return al
}

func (a *ipAllowlist) allows(ipStr string) bool {
	if a == nil {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, allowedIP := range a.ips {
		if allowedIP.Equal(ip) {
			return true
		}
	}
	for _, cidr := range a.nets {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func clientIP(r *http.Request, trustProxy bool) string {
	if trustProxy {
		if xff := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); xff != "" {
			first := strings.TrimSpace(strings.Split(xff, ",")[0])
			if net.ParseIP(first) != nil {
				return first
			}
		}
		if xri := strings.TrimSpace(r.Header.Get("X-Real-IP")); xri != "" {
			if net.ParseIP(xri) != nil {
				return xri
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

type authenticator struct {
	tokens        [][]byte
	allowInsecure bool
	hmacSecret    []byte
	maxSkewSec    int64
	nonceCache    *cache.TTLCache[struct{}]
}

func newAuthenticator(cfg config.Config) *authenticator {
	toks := make([][]byte, 0, len(cfg.AuthTokens))
	for _, t := range cfg.AuthTokens {
		if trimmed := strings.TrimSpace(t); trimmed != "" {
			toks = append(toks, []byte(trimmed))
		}
	}
	skew := cfg.HMACMaxSkewSeconds
	if skew <= 0 {
		skew = 300
	}
	return &authenticator{
		tokens:        toks,
		allowInsecure: cfg.AllowInsecureNoAuth,
		hmacSecret:    []byte(cfg.HMACSecret),
		maxSkewSec:    skew,
		nonceCache:    cache.NewTTLCache[struct{}](10000),
	}
}

func (a *authenticator) authenticate(r *http.Request) (string, *service.GatewayError) {
	if len(a.tokens) == 0 {
		if !a.allowInsecure {
			return "", service.NewUnauthorized("未配置服务端鉴权 Token")
		}
	} else {
		token := extractClientToken(r)
		if token == "" {
			return "", service.NewUnauthorized("缺少认证凭据 (Authorization: Bearer 或 X-API-Key)")
		}
		matched := false
		tokBytes := []byte(token)
		for _, candidate := range a.tokens {
			if len(tokBytes) == len(candidate) && subtle.ConstantTimeCompare(tokBytes, candidate) == 1 {
				matched = true
			}
		}
		if !matched {
			return "", service.NewUnauthorized("认证 Token 无效")
		}

		// Hash token to avoid keeping raw token in memory maps/metrics
		sum := sha256.Sum256(tokBytes)
		tokenID := hex.EncodeToString(sum[:8])
		if len(a.hmacSecret) > 0 {
			if err := a.verifyHMAC(r); err != nil {
				return "", err
			}
		}
		return tokenID, nil
	}

	if len(a.hmacSecret) > 0 {
		if err := a.verifyHMAC(r); err != nil {
			return "", err
		}
	}
	return "anonymous", nil
}

func (a *authenticator) verifyHMAC(r *http.Request) *service.GatewayError {
	tsStr := strings.TrimSpace(r.Header.Get("X-Timestamp"))
	nonce := strings.TrimSpace(r.Header.Get("X-Nonce"))
	sigHex := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Signature")))
	if tsStr == "" || len(nonce) < 16 || sigHex == "" {
		return service.NewUnauthorized("缺少或非法的请求签名头 (X-Timestamp / X-Nonce / X-Signature)")
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return service.NewUnauthorized("X-Timestamp 非法")
	}
	nowSec := time.Now().Unix()
	diff := nowSec - ts
	if diff < 0 {
		diff = -diff
	}
	if diff > a.maxSkewSec {
		return service.NewUnauthorized("请求签名已过期")
	}
	nonceKey := tsStr + ":" + nonce
	if _, seen := a.nonceCache.Get(nonceKey); seen {
		return service.NewUnauthorized("请求 Nonce 重放")
	}

	var bodyBytes []byte
	if r.Body != nil {
		var readErr error
		bodyBytes, readErr = io.ReadAll(io.LimitReader(r.Body, 2<<20))
		_ = r.Body.Close()
		if readErr != nil {
			return service.NewBadRequest("读取请求体失败")
		}
		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	bodyHash := sha256.Sum256(bodyBytes)
	canonical := r.Method + "\n" + r.URL.Path + "\n" + tsStr + "\n" + nonce + "\n" + hex.EncodeToString(bodyHash[:])

	mac := hmac.New(sha256.New, a.hmacSecret)
	mac.Write([]byte(canonical))
	expected := hex.EncodeToString(mac.Sum(nil))
	if len(sigHex) != len(expected) || subtle.ConstantTimeCompare([]byte(sigHex), []byte(expected)) != 1 {
		return service.NewUnauthorized("请求签名验证失败")
	}

	a.nonceCache.Set(nonceKey, struct{}{}, time.Duration(a.maxSkewSec*2)*time.Second)
	return nil
}

func extractClientToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	if apiKey := strings.TrimSpace(r.Header.Get("X-API-Key")); apiKey != "" {
		return apiKey
	}
	return ""
}

type tokenBucket struct {
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

type rateLimiter struct {
	mu          sync.Mutex
	tokenQPS    float64
	tokenConc   int
	ipQPS       float64
	tokenState  map[string]*tokenBucket
	ipState     map[string]*tokenBucket
	concurrency map[string]int
}

func newRateLimiter(tokenQPS float64, tokenConc int, ipQPS float64) *rateLimiter {
	if tokenQPS <= 0 {
		tokenQPS = 20
	}
	if tokenConc <= 0 {
		tokenConc = 8
	}
	if ipQPS <= 0 {
		ipQPS = 50
	}
	return &rateLimiter{
		tokenQPS:    tokenQPS,
		tokenConc:   tokenConc,
		ipQPS:       ipQPS,
		tokenState:  make(map[string]*tokenBucket),
		ipState:     make(map[string]*tokenBucket),
		concurrency: make(map[string]int),
	}
}

func (rl *rateLimiter) acquire(tokenID, ip string) (release func(), retryAfterSec int, ok bool) {
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if !takeFromBucket(rl.ipState, ip, rl.ipQPS, now) {
		return nil, 2, false
	}
	if tokenID != "" {
		if !takeFromBucket(rl.tokenState, tokenID, rl.tokenQPS, now) {
			return nil, 2, false
		}
		if rl.concurrency[tokenID] >= rl.tokenConc {
			return nil, 2, false
		}
		rl.concurrency[tokenID]++
		var once sync.Once
		return func() {
			once.Do(func() {
				rl.mu.Lock()
				if rl.concurrency[tokenID] > 1 {
					rl.concurrency[tokenID]--
				} else {
					delete(rl.concurrency, tokenID)
				}
				rl.mu.Unlock()
			})
		}, 0, true
	}

	return func() {}, 0, true
}

func takeFromBucket(m map[string]*tokenBucket, key string, qps float64, now time.Time) bool {
	if len(m) > 8192 {
		cutoff := now.Add(-5 * time.Minute)
		for k, b := range m {
			if b.lastSeen.Before(cutoff) {
				delete(m, k)
			}
		}
	}
	burst := math.Max(1, qps)
	b, ok := m[key]
	if !ok {
		b = &tokenBucket{
			tokens:     burst - 1,
			lastRefill: now,
			lastSeen:   now,
		}
		m[key] = b
		return true
	}
	elapsed := now.Sub(b.lastRefill).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(burst, b.tokens+elapsed*qps)
		b.lastRefill = now
	}
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens -= 1
	return true
}
