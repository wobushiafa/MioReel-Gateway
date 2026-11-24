package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"mioreel-gateway/server/internal/cache"
)

type mediaTicketPayload struct {
	VID           string
	PlayURL       string
	DecryptionKey string
	UserAgent     string
	ExpiresAtMs   int64
}

// MediaService issues signed one-time/short-lived tickets and serves pre-decrypted MP4 streams with Range support.
type MediaService struct {
	enabled    bool
	secret     []byte
	ffmpegPath string
	cacheDir   string
	tickets    *cache.TTLCache[mediaTicketPayload]

	mu       sync.Mutex
	inFlight map[string]*sync.Mutex
}

func NewMediaService(enabled bool, secret, ffmpegPath, cacheDir string) *MediaService {
	var sec []byte
	if secret != "" {
		sec = []byte(secret)
	} else {
		sec = make([]byte, 32)
		_, _ = io.ReadFull(rand.Reader, sec)
	}
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if cacheDir == "" {
		cacheDir = filepath.Join("data", "media_cache")
	}
	return &MediaService{
		enabled:    enabled,
		secret:     sec,
		ffmpegPath: ffmpegPath,
		cacheDir:   cacheDir,
		tickets:    cache.NewTTLCache[mediaTicketPayload](1024),
		inFlight:   make(map[string]*sync.Mutex),
	}
}

func (m *MediaService) Enabled() bool {
	return m != nil && m.enabled
}

// IssueTicket creates an HMAC-authenticated ticket keeping play_url and decryption_key server-side.
func (m *MediaService) IssueTicket(vid, playURL, decryptionKey, userAgent string, expiresAtMs int64) string {
	if !m.Enabled() {
		return ""
	}
	var rnd [12]byte
	_, _ = io.ReadFull(rand.Reader, rnd[:])
	id := hex.EncodeToString(rnd[:])
	ttl := time.Until(time.UnixMilli(expiresAtMs))
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	m.tickets.Set(id, mediaTicketPayload{
		VID:           vid,
		PlayURL:       playURL,
		DecryptionKey: decryptionKey,
		UserAgent:     userAgent,
		ExpiresAtMs:   expiresAtMs,
	}, ttl)

	payload := id + "." + strconv.FormatInt(expiresAtMs, 10)
	sig := m.sign(payload)
	return base64.RawURLEncoding.EncodeToString([]byte(payload + "." + sig))
}

// ServeMedia handles GET /api/v1/media/{ticket} with Range support.
func (m *MediaService) ServeMedia(w http.ResponseWriter, r *http.Request, rawTicket string) error {
	if !m.Enabled() {
		return NewForbidden("媒体代理未启用")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(rawTicket))
	if err != nil {
		return NewUnauthorized("无效的媒体票据")
	}
	parts := strings.Split(string(decoded), ".")
	if len(parts) != 3 {
		return NewUnauthorized("无效的媒体票据格式")
	}
	id, expStr, sig := parts[0], parts[1], parts[2]
	if !hmac.Equal([]byte(sig), []byte(m.sign(id+"."+expStr))) {
		return NewUnauthorized("媒体票据签名无效")
	}
	expMs, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().UnixMilli() > expMs {
		return NewUnauthorized("媒体票据已过期")
	}

	item, ok := m.tickets.Get(id)
	if !ok {
		return NewNotFound("媒体票据不存在或已失效", "MEDIA_URL_EXPIRED")
	}

	filePath, err := m.ensureDecryptedMP4(r.Context(), item)
	if err != nil {
		return err
	}
	f, err := os.Open(filePath)
	if err != nil {
		return MapError(err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return MapError(err)
	}
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "private, max-age=60")
	http.ServeContent(w, r, item.VID+".mp4", st.ModTime(), f)
	return nil
}

func (m *MediaService) ensureDecryptedMP4(ctx context.Context, item mediaTicketPayload) (string, error) {
	if err := os.MkdirAll(m.cacheDir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(item.VID + "|" + item.DecryptionKey))
	key := hex.EncodeToString(sum[:16])
	finalPath := filepath.Join(m.cacheDir, key+".mp4")

	lock := m.getLock(key)
	lock.Lock()
	defer lock.Unlock()

	if st, err := os.Stat(finalPath); err == nil && st.Size() > 0 {
		return finalPath, nil
	}

	tmpPath := finalPath + ".tmp.mp4"
	_ = os.Remove(tmpPath)

	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if item.DecryptionKey != "" {
		args = append(args, "-decryption_key", item.DecryptionKey)
	}
	if item.UserAgent != "" {
		args = append(args, "-user_agent", item.UserAgent)
	}
	args = append(args, "-i", item.PlayURL, "-c", "copy", "-movflags", "+faststart", tmpPath)

	remuxCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(remuxCtx, m.ffmpegPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("ffmpeg pre-decrypt failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", errors.New("failed to persist pre-decrypted media")
	}
	return finalPath, nil
}

func (m *MediaService) getLock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.inFlight[key]
	if !ok {
		l = &sync.Mutex{}
		m.inFlight[key] = l
	}
	return l
}

func (m *MediaService) sign(payload string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
