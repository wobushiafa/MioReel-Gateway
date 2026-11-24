package observability

import (
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
)

var sensitiveKeyNames = map[string]struct{}{
	"play_url":       {},
	"decryption_key": {},
	"contentkey":     {},
	"content_key":    {},
	"authorization":  {},
	"x-api-key":      {},
	"x-gorgon":       {},
	"x-medusa":       {},
	"x-helios":       {},
	"x-ladon":        {},
	"x-argus":        {},
	"x-khronos":      {},
	"x-ss-stub":      {},
	"cookie":         {},
	"set-cookie":     {},
	"access_token":   {},
	"refresh_token":  {},
	"client_secret":  {},
	"material":       {},
	"hmac_secret":    {},
}

var inlineSecretPattern = regexp.MustCompile(`(?i)(authorization|cookie|set-cookie|decryption_key|contentKey|play_url|access_token|refresh_token|client_secret|x-gorgon|x-medusa|x-helios)\s*[:=]\s*(?:Bearer\s+)?("[^"]*"|'[^']*'|[^\s,;]+)`)

// RedactString masks sensitive key=value fragments inside free-form messages.
func RedactString(s string) string {
	return inlineSecretPattern.ReplaceAllString(s, `$1=[REDACTED]`)
}

// NewLogger builds a JSON slog.Logger that automatically redacts sensitive attributes
// (play_url, decryption_key, signing headers, Authorization, tokens, etc.).
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	if w == nil {
		w = os.Stdout
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Key = "ts"
			}
			keyLower := strings.ToLower(a.Key)
			if _, sensitive := sensitiveKeyNames[keyLower]; sensitive {
				return slog.String(a.Key, "[REDACTED]")
			}
			if a.Value.Kind() == slog.KindString {
				return slog.String(a.Key, RedactString(a.Value.String()))
			}
			return a
		},
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
