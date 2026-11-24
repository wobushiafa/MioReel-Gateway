package profile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/observability"
	"mioreel-gateway/server/internal/signer"
)

type externalProfilePayload struct {
	ID                string `json:"id"`
	Material          string `json:"material"`
	SDKVersion        string `json:"sdk_version"`
	CamelSDK          string `json:"sdkVersion"`
	ProfileID         string `json:"profile_id"`
	ProfileMaterial   string `json:"profile_material"`
	ProfileSDKVersion string `json:"profile_sdk_version"`
}

// Manager maintains the active signing profile with validation and background hot-reload.
type Manager struct {
	cfg     config.Config
	logger  *slog.Logger
	metrics *observability.Metrics
	httpCli *http.Client

	mu        sync.RWMutex
	active    *signer.Profile
	rotatedAt time.Time
	source    string
	degraded  bool
}

// NewManager initializes the signing profile using priority:
// env (MIOREEL_PROFILE_ID/MATERIAL) > remote URL > local file > built-in fallback.
func NewManager(cfg config.Config, logger *slog.Logger, metrics *observability.Metrics) *Manager {
	if logger == nil {
		logger = observability.NewLogger(nil, slog.LevelInfo)
	}
	m := &Manager{
		cfg:     cfg,
		logger:  logger,
		metrics: metrics,
		httpCli: &http.Client{Timeout: 8 * time.Second},
	}

	// Always initialize built-in fallback first so service never starts without a profile.
	m.active = signer.BuiltInProfile(signer.CryptoRand{})
	m.rotatedAt = time.Now().UTC()
	m.source = "builtin"
	if m.metrics != nil {
		m.metrics.SetProfileRotatedTimestamp(m.rotatedAt.Unix())
	}

	_ = m.Refresh(context.Background())
	return m
}

// Current returns the currently active signing profile.
func (m *Manager) Current() *signer.Profile {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active
}

// Status returns profile metadata for /api/v1/health.
func (m *Manager) Status() (profileID, sdkVersion string, rotatedAt time.Time, degraded bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active.ID(), m.active.SDKVersion(), m.rotatedAt, m.degraded
}

// StartRefresher periodically reloads external signing material until ctx is cancelled.
func (m *Manager) StartRefresher(ctx context.Context) {
	if m.cfg.ProfileURL == "" && m.cfg.ProfileFile == "" {
		return
	}
	interval := m.cfg.ProfileRefreshInterval
	if interval <= 0 {
		interval = 10 * time.Minute
	}
	ticker := time.NewTicker(interval)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := m.Refresh(ctx); err != nil {
					m.logger.Warn("profile refresh failed, retaining previous configuration",
						slog.String("error", err.Error()),
					)
				}
			}
		}
	}()
}

// Refresh attempts to load external profile material according to configured priority.
func (m *Manager) Refresh(ctx context.Context) error {
	// 1. Env override
	if m.cfg.ProfileID != "" || m.cfg.ProfileMaterial != "" {
		sdk := m.cfg.ProfileSDKVersion
		if sdk == "" {
			sdk = signer.SDKVersion
		}
		if err := m.applyRaw("env", m.cfg.ProfileID, m.cfg.ProfileMaterial, sdk); err != nil {
			m.markDegraded(true)
			m.logger.Warn("env signing profile invalid, keeping previous profile", slog.String("error", err.Error()))
			return err
		}
		return nil
	}

	// 2. HTTP config center
	if m.cfg.ProfileURL != "" {
		id, mat, sdk, err := m.fetchRemote(ctx, m.cfg.ProfileURL)
		if err == nil {
			if err := m.applyRaw("remote", id, mat, sdk); err == nil {
				return nil
			} else {
				err = fmt.Errorf("remote validation: %w", err)
				m.markDegraded(true)
				return err
			}
		} else {
			m.logger.Warn("remote profile fetch failed", slog.String("error", err.Error()))
		}
	}

	// 3. Local file
	if m.cfg.ProfileFile != "" {
		id, mat, sdk, err := m.readFile(m.cfg.ProfileFile)
		if err != nil {
			m.markDegraded(true)
			return err
		}
		if err := m.applyRaw("file", id, mat, sdk); err != nil {
			m.markDegraded(true)
			return err
		}
		return nil
	}

	return nil
}

func (m *Manager) markDegraded(v bool) {
	m.mu.Lock()
	m.degraded = v
	m.mu.Unlock()
}

func (m *Manager) applyRaw(source, idB64, matB64, sdkVer string) error {
	if sdkVer == "" {
		sdkVer = signer.SDKVersion
	}
	idBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(idB64))
	if err != nil {
		return fmt.Errorf("decode profile id base64: %w", err)
	}
	matBytes, err := base64.StdEncoding.DecodeString(strings.TrimSpace(matB64))
	if err != nil {
		return fmt.Errorf("decode profile material base64: %w", err)
	}
	if len(idBytes) < 1 || len(idBytes) > 128 {
		return errors.New("profile id decoded length must be in [1, 128]")
	}
	if len(matBytes) != 32 {
		return fmt.Errorf("profile material decoded length=%d != 32", len(matBytes))
	}

	prof, err := signer.NewSigningProfile(signer.Configuration{
		ID:       idBytes,
		Material: matBytes,
	}, sdkVer, signer.CryptoRand{})
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	m.mu.Lock()
	changed := m.active == nil || m.active.ID() != prof.ID() || m.source != source
	m.active = prof
	m.degraded = false
	if changed {
		m.rotatedAt = now
		m.source = source
		if m.metrics != nil {
			m.metrics.SetProfileRotatedTimestamp(now.Unix())
		}
	}
	m.mu.Unlock()

	if changed {
		m.logger.Info("signing profile updated",
			slog.String("source", source),
			slog.String("profile_id", prof.ID()),
			slog.String("sdk_version", prof.SDKVersion()),
		)
	}
	return nil
}

func (m *Manager) fetchRemote(ctx context.Context, rawURL string) (string, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := m.httpCli.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	return parseProfilePayload(body)
}

func (m *Manager) readFile(path string) (string, string, string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", "", "", err
	}
	return parseProfilePayload(body)
}

func parseProfilePayload(body []byte) (string, string, string, error) {
	var p externalProfilePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return "", "", "", err
	}
	id := firstNonEmpty(p.ProfileID, p.ID)
	mat := firstNonEmpty(p.ProfileMaterial, p.Material)
	sdk := firstNonEmpty(p.ProfileSDKVersion, p.SDKVersion, p.CamelSDK)
	return id, mat, sdk, nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
