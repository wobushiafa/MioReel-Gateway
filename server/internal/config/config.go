package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime settings for mioreel-gateway.
type Config struct {
	ListenAddr string `json:"listen_addr"`
	Version    string `json:"version"`

	// Security & Auth
	AuthTokens          []string `json:"auth_tokens"`
	AllowInsecureNoAuth bool     `json:"allow_insecure_no_auth"`
	HMACSecret          string   `json:"hmac_secret"`
	HMACMaxSkewSeconds  int64    `json:"hmac_max_skew_seconds"`
	IPAllowlist         []string `json:"ip_allowlist"`
	TrustProxyHeaders   bool     `json:"trust_proxy_headers"`

	// Rate limits (defaults per docs/01-architecture.md §7)
	TokenQPS         float64 `json:"token_qps"`
	TokenConcurrency int     `json:"token_concurrency"`
	IPQPS            float64 `json:"ip_qps"`

	// Signing profile hot-reload
	ProfileID              string        `json:"profile_id"`
	ProfileMaterial        string        `json:"profile_material"`
	ProfileSDKVersion      string        `json:"profile_sdk_version"`
	ProfileFile            string        `json:"profile_file"`
	ProfileURL             string        `json:"profile_url"`
	ProfileRefreshInterval time.Duration `json:"profile_refresh_interval"`

	// Device & persistence
	DeviceStorePath string `json:"device_store_path"`

	// Timeouts
	DefaultResolveTimeoutMs int `json:"default_resolve_timeout_ms"`

	// Optional Web pre-decrypted media proxy (/api/v1/media/{ticket})
	MediaProxyEnabled bool   `json:"media_proxy_enabled"`
	MediaTicketSecret string `json:"media_ticket_secret"`
	MediaFFmpegPath   string `json:"media_ffmpeg_path"`
	MediaCacheDir     string `json:"media_cache_dir"`
}

// Default returns production-ready defaults matching the gateway documentation.
func Default() Config {
	return Config{
		ListenAddr:              ":18662",
		Version:                 "1.0.0",
		HMACMaxSkewSeconds:      300,
		TokenQPS:                20,
		TokenConcurrency:        8,
		IPQPS:                   50,
		ProfileSDKVersion:       "v04.10.03-ml-android",
		ProfileRefreshInterval:  10 * time.Minute,
		DeviceStorePath:         "data/device.json",
		DefaultResolveTimeoutMs: 15000,
		MediaProxyEnabled:       false,
		MediaFFmpegPath:         "ffmpeg",
		MediaCacheDir:           "data/media_cache",
	}
}

// Load reads an optional JSON config file and overlays MIOREEL_* environment variables.
func Load(path string) (Config, error) {
	cfg := Default()
	if path == "" {
		path = strings.TrimSpace(os.Getenv("MIOREEL_CONFIG_FILE"))
	}
	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %s: %w", path, err)
		}
		type configAlias Config
		var aux struct {
			configAlias
			ProfileRefreshIntervalRaw json.RawMessage `json:"profile_refresh_interval"`
		}
		aux.configAlias = configAlias(cfg)
		if err := json.Unmarshal(raw, &aux); err != nil {
			return cfg, fmt.Errorf("parse config %s: %w", path, err)
		}
		cfg = Config(aux.configAlias)
		if len(aux.ProfileRefreshIntervalRaw) > 0 && !bytes.Equal(bytes.TrimSpace(aux.ProfileRefreshIntervalRaw), []byte("null")) {
			d, err := parseFlexDuration(aux.ProfileRefreshIntervalRaw)
			if err != nil {
				return cfg, fmt.Errorf("parse config %s profile_refresh_interval: %w", path, err)
			}
			cfg.ProfileRefreshInterval = d
		}
	}

	if v := strings.TrimSpace(os.Getenv("MIOREEL_LISTEN_ADDR")); v != "" {
		cfg.ListenAddr = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_AUTH_TOKENS")); v != "" {
		cfg.AuthTokens = splitCSV(v)
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_ALLOW_INSECURE_NO_AUTH")); v != "" {
		cfg.AllowInsecureNoAuth = parseBool(v)
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_HMAC_SECRET")); v != "" {
		cfg.HMACSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_IP_ALLOWLIST")); v != "" {
		cfg.IPAllowlist = splitCSV(v)
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_TRUST_PROXY_HEADERS")); v != "" {
		cfg.TrustProxyHeaders = parseBool(v)
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_TOKEN_QPS")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.TokenQPS = f
		}
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_TOKEN_CONCURRENCY")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.TokenConcurrency = n
		}
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_IP_QPS")); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.IPQPS = f
		}
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_ID")); v != "" {
		cfg.ProfileID = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_MATERIAL")); v != "" {
		cfg.ProfileMaterial = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_SDK_VERSION")); v != "" {
		cfg.ProfileSDKVersion = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_FILE")); v != "" {
		cfg.ProfileFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_URL")); v != "" {
		cfg.ProfileURL = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_PROFILE_REFRESH_INTERVAL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.ProfileRefreshInterval = d
		}
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_DEVICE_STORE_PATH")); v != "" {
		cfg.DeviceStorePath = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_MEDIA_PROXY_ENABLED")); v != "" {
		cfg.MediaProxyEnabled = parseBool(v)
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_MEDIA_TICKET_SECRET")); v != "" {
		cfg.MediaTicketSecret = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_MEDIA_FFMPEG_PATH")); v != "" {
		cfg.MediaFFmpegPath = v
	}
	if v := strings.TrimSpace(os.Getenv("MIOREEL_MEDIA_CACHE_DIR")); v != "" {
		cfg.MediaCacheDir = v
	}
	return cfg, nil
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func parseBool(s string) bool {
	b, _ := strconv.ParseBool(strings.ToLower(strings.TrimSpace(s)))
	return b
}

func parseFlexDuration(raw json.RawMessage) (time.Duration, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return 0, nil
		}
		return time.ParseDuration(s)
	}
	var ns int64
	if err := json.Unmarshal(raw, &ns); err == nil {
		return time.Duration(ns), nil
	}
	return 0, fmt.Errorf("expected duration string (e.g. \"10m\") or integer nanoseconds")
}

// ResolveListenAddr applies CLI -listen and -port overrides on top of config/env listen_addr.
func ResolveListenAddr(baseAddr, listenFlag string, portFlag int) (string, error) {
	addr := strings.TrimSpace(baseAddr)
	if addr == "" {
		addr = ":18662"
	}
	if lf := strings.TrimSpace(listenFlag); lf != "" {
		if _, err := strconv.Atoi(lf); err == nil {
			addr = ":" + lf
		} else {
			addr = lf
		}
	}
	if portFlag != 0 {
		if portFlag < 1 || portFlag > 65535 {
			return "", fmt.Errorf("invalid port %d: must be in 1..65535", portFlag)
		}
		host := ""
		if h, _, err := net.SplitHostPort(addr); err == nil {
			host = h
		} else if !strings.Contains(addr, ":") {
			host = addr
		}
		addr = net.JoinHostPort(host, strconv.Itoa(portFlag))
	}
	if _, portStr, err := net.SplitHostPort(addr); err != nil || portStr == "" {
		return "", fmt.Errorf("invalid listen address %q (expected host:port or :port)", addr)
	}
	return addr, nil
}
