package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLoggerRedactsSensitiveFields(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, slog.LevelInfo)

	logger.Info("resolve",
		slog.String("play_url", "https://cdn.example/secret.mp4"),
		slog.String("decryption_key", "982735c47051044342ede4ef5d3e8cb5"),
		slog.String("x-gorgon", "8404deadbeef"),
		slog.String("x-medusa", "9JT6secret"),
		slog.String("Authorization", "Bearer supersecret"),
		slog.String("msg", "header authorization=Bearer tok123 failed"),
		slog.String("series_id", "7701678491724418073"),
	)

	out := buf.String()
	for _, secret := range []string{
		"https://cdn.example/secret.mp4",
		"982735c47051044342ede4ef5d3e8cb5",
		"8404deadbeef",
		"9JT6secret",
		"supersecret",
		"tok123",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("log output leaked secret %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "7701678491724418073") {
		t.Fatalf("expected non-sensitive series_id in log output: %s", out)
	}
}
