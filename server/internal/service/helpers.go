package service

import (
	"regexp"
	"strings"
)

var (
	id10to30Pattern = regexp.MustCompile(`^\d{10,30}$`)
	novelPicPattern = regexp.MustCompile(`novel-pic/([a-zA-Z0-9_-]+)`)
	htmlTagPattern  = regexp.MustCompile(`<[^>]+>`)
)

func isValidID(s string) bool {
	return id10to30Pattern.MatchString(strings.TrimSpace(s))
}

func cleanText(s string) string {
	if s == "" {
		return ""
	}
	return strings.TrimSpace(htmlTagPattern.ReplaceAllString(s, ""))
}

// cleanCoverURL matches cleanCoverUrl() from REF L467.
func cleanCoverURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if m := novelPicPattern.FindStringSubmatch(trimmed); len(m) == 2 {
		return "https://p3-novel.byteimg.com/novel-pic/" + m[1] + "~tplv-shrink:640:0.image"
	}
	if strings.Contains(trimmed, ".heic") {
		return strings.ReplaceAll(trimmed, ".heic", ".image")
	}
	return trimmed
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}
