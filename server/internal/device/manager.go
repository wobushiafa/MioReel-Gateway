package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"mioreel-gateway/server/internal/signer"
	"mioreel-gateway/server/internal/upstream"
)

const deviceRegisterURL = "https://log.snssdk.com/service/2/device_register/?aid=8662&app_name=novelread"

var idDigitsPattern = regexp.MustCompile(`^\d{10,24}$`)

type persistedDevice struct {
	IID      string `json:"iid"`
	DeviceID string `json:"device_id"`
	CDID     string `json:"cdid"`
}

// Manager handles guest device registration, validation, in-flight deduplication, and persistence.
type Manager struct {
	client    *upstream.Client
	storePath string
	logger    *slog.Logger

	mu       sync.Mutex
	cached   *signer.Device
	inflight chan struct{}
	lastErr  error
}

func NewManager(client *upstream.Client, storePath string, logger *slog.Logger) *Manager {
	if client == nil {
		client = upstream.NewClient()
	}
	m := &Manager{
		client:    client,
		storePath: storePath,
		logger:    logger,
	}
	m.loadFromDisk()
	return m
}

// Ready reports whether a valid device triple is currently cached.
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cached != nil && isValidDevice(*m.cached)
}

// Reset clears the cached device so the next Get() re-registers.
func (m *Manager) Reset() {
	m.mu.Lock()
	m.cached = nil
	m.mu.Unlock()
	if m.storePath != "" {
		_ = os.Remove(m.storePath)
	}
}

// Get returns a valid device triple, registering a new guest session if needed.
func (m *Manager) Get(ctx context.Context, deadline time.Time) (signer.Device, error) {
	for {
		m.mu.Lock()
		if m.cached != nil && isValidDevice(*m.cached) {
			dev := *m.cached
			m.mu.Unlock()
			return dev, nil
		}
		if m.inflight != nil {
			ch := m.inflight
			m.mu.Unlock()
			select {
			case <-ch:
				m.mu.Lock()
				if m.cached != nil && isValidDevice(*m.cached) {
					dev := *m.cached
					m.mu.Unlock()
					return dev, nil
				}
				err := m.lastErr
				m.mu.Unlock()
				if err != nil {
					return signer.Device{}, err
				}
				continue
			case <-ctx.Done():
				return signer.Device{}, ctx.Err()
			}
		}

		ch := make(chan struct{})
		m.inflight = ch
		m.lastErr = nil
		m.mu.Unlock()

		dev, err := m.register(ctx, deadline)

		m.mu.Lock()
		if err == nil {
			m.cached = &dev
			m.saveToDisk(dev)
		} else {
			m.lastErr = err
		}
		m.inflight = nil
		close(ch)
		m.mu.Unlock()

		return dev, err
	}
}

func (m *Manager) register(ctx context.Context, deadline time.Time) (signer.Device, error) {
	cdid, err := newUUIDv4()
	if err != nil {
		return signer.Device{}, err
	}
	openudid, err := newHexBytes(8)
	if err != nil {
		return signer.Device{}, err
	}

	payload := map[string]any{
		"magic_tag": "ss_app_log",
		"header": map[string]any{
			"aid":                   8662,
			"app_name":              "novelread",
			"package":               "com.phoenix.read",
			"app_version":           "7.3.9.32",
			"version_code":          73932,
			"update_version_code":   73932,
			"manifest_version_code": 73932,
			"sdk_version":           "6.18.0",
			"device_platform":       "android",
			"os":                    "Android",
			"os_version":            "16",
			"os_api":                36,
			"device_model":          "25053RT47C",
			"device_brand":          "Redmi",
			"device_manufacturer":   "Xiaomi",
			"density_dpi":           520,
			"display_density":       "mdpi",
			"resolution":            "1280x2772",
			"language":              "zh",
			"timezone":              8,
			"access":                "wifi",
			"channel":               "update_64",
			"cdid":                  cdid,
			"openudid":              openudid,
		},
		"_gen_time": time.Now().UnixMilli(),
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return signer.Device{}, err
	}

	regDeadline := time.Now().Add(12 * time.Second)
	if !deadline.IsZero() && deadline.Before(regDeadline) {
		regDeadline = deadline
	}

	text, err := m.client.DoText(ctx, "POST", deviceRegisterURL, map[string]string{
		"Content-Type": "application/json; charset=utf-8",
		"User-Agent":   signer.AppUserAgent,
	}, bodyBytes, regDeadline)
	if err != nil {
		return signer.Device{}, err
	}

	var resp struct {
		InstallIDStr string `json:"install_id_str"`
		DeviceIDStr  string `json:"device_id_str"`
	}
	if err := json.Unmarshal([]byte(text), &resp); err != nil {
		return signer.Device{}, &upstream.UpstreamError{Code: "INVALID_RESPONSE", Message: "device_register parse failed", Err: err}
	}
	if !idDigitsPattern.MatchString(resp.InstallIDStr) || !idDigitsPattern.MatchString(resp.DeviceIDStr) {
		return signer.Device{}, &upstream.UpstreamError{Code: "GUEST_REGISTRATION_FAILED", Message: "invalid iid or device_id from device_register"}
	}

	dev := signer.Device{
		IID:      resp.InstallIDStr,
		DeviceID: resp.DeviceIDStr,
		CDID:     cdid,
	}
	if m.logger != nil {
		m.logger.Info("registered upstream guest device",
			slog.String("iid", dev.IID),
			slog.String("device_id", dev.DeviceID),
		)
	}
	return dev, nil
}

func isValidDevice(d signer.Device) bool {
	return idDigitsPattern.MatchString(d.IID) && idDigitsPattern.MatchString(d.DeviceID) && d.CDID != ""
}

func (m *Manager) loadFromDisk() {
	if m.storePath == "" {
		return
	}
	raw, err := os.ReadFile(m.storePath)
	if err != nil {
		return
	}
	var p persistedDevice
	if err := json.Unmarshal(raw, &p); err != nil {
		return
	}
	dev := signer.Device{IID: p.IID, DeviceID: p.DeviceID, CDID: p.CDID}
	if isValidDevice(dev) {
		m.cached = &dev
	}
}

func (m *Manager) saveToDisk(dev signer.Device) {
	if m.storePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.storePath), 0o755); err != nil {
		return
	}
	raw, err := json.MarshalIndent(persistedDevice{
		IID:      dev.IID,
		DeviceID: dev.DeviceID,
		CDID:     dev.CDID,
	}, "", "  ")
	if err != nil {
		return
	}
	tmp := m.storePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, m.storePath)
	}
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]), nil
}

func newHexBytes(n int) (string, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
