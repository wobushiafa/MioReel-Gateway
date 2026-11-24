package resolver

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/bits"
	"net/url"
	"regexp"
	"strings"
)

var (
	ErrInvalidResponse = errors.New("INVALID_RESPONSE")
	ErrSecurityBlocked = errors.New("SECURITY_BLOCKED")
)

// DomainError carries a structured upstream-compatible error code (e.g. SECURITY_BLOCKED, INVALID_RESPONSE).
type DomainError struct {
	Code    string
	Message string
}

func (e *DomainError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

var (
	hex32Pattern = regexp.MustCompile(`^[0-9a-fA-F]{32}$`)
	spadeURLSalt = func() []byte {
		b, err := hex.DecodeString("4dd4c2e6b83162090e52b3c7a6733ba41cb2462b829ab58a196b39db57177524f49baf7f08e8d68d26a72e37c1a95a2f1f05a51892aef2949732b62a38aadd58")
		if err != nil {
			panic(err)
		}
		return b
	}()
)

func decodeBase64Flex(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "-", "+")
	s = strings.ReplaceAll(s, "_", "/")
	s = strings.TrimRight(s, "=")
	return base64.RawStdEncoding.DecodeString(s)
}

// DecodeSpadeKey implements no(spade_a) from REF L1008, returning a 32-char lowercase hex AES-128 key.
func DecodeSpadeKey(spadeA string) (string, error) {
	e, err := decodeBase64Flex(spadeA)
	if err != nil {
		return "", fmt.Errorf("spade_a base64 invalid: %w", err)
	}
	if len(e) < 3 {
		return "", errors.New("spade_a too short")
	}
	t := len(e) - int(e[0]^e[1]^e[2]) + 47
	if t > len(e)-1 {
		t = len(e) - 1
	}
	if t < 33 {
		return "", errors.New("spade_a key length invalid")
	}
	n := make([]byte, t)
	copy(n, e[1:1+t])

	var i, o byte = 85, 246
	for c := 0; c < t; c++ {
		s := n[c]
		u := o
		if (c & 1) != 0 {
			u = i
			i = s
		} else {
			o = s
		}
		f := bits.OnesCount32(uint32(c))
		n[c] = byte((-21 - f + int(u^s)) & 255)
	}

	keyHex := string(n[1:33])
	if !hex32Pattern.MatchString(keyHex) {
		return "", errors.New("spade_a decoded key is not 32 hex chars")
	}
	raw, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// DecodeSpadeURL implements io(cipherText, keySeed) from REF L1008.
func DecodeSpadeURL(cipherText string, keySeed []byte) (string, error) {
	t := strings.TrimSpace(cipherText)
	if t == "" {
		return "", nil
	}
	n, err := decodeBase64Flex(t)
	if err != nil {
		return "", fmt.Errorf("spade URL base64 invalid: %w", err)
	}
	if len(n) < 5 {
		return "", errors.New("spade URL ciphertext too short")
	}
	if n[0] != 168 || n[2] != 1 || n[3] != 0 {
		return "", errors.New("spade URL header format error")
	}
	complete := ((len(n) - 4) / 16) * 16
	if complete == 0 {
		return "", errors.New("spade URL ciphertext has no complete block")
	}

	a := sha512.Sum512(keySeed)
	mix := make([]byte, 0, len(a)+len(spadeURLSalt))
	mix = append(mix, a[:]...)
	mix = append(mix, spadeURLSalt...)
	c := sha512.Sum512(mix)

	blk, err := aes.NewCipher(c[0:16])
	if err != nil {
		return "", err
	}
	plain := make([]byte, complete)
	cipher.NewCBCDecrypter(blk, c[16:32]).CryptBlocks(plain, n[4:4+complete])

	if len(plain) > 0 {
		pad := int(plain[len(plain)-1])
		if pad >= 1 && pad <= 16 && pad <= len(plain) {
			plain = plain[:len(plain)-pad]
		}
	}
	for len(plain) > 0 && plain[len(plain)-1] == 0 {
		plain = plain[:len(plain)-1]
	}
	return string(plain), nil
}

// UnescapeURL implements cn(s) from REF L91266.
func UnescapeURL(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, `\/`, `/`)
	s = strings.ReplaceAll(s, `\u0026`, `&`)
	s = strings.ReplaceAll(s, `&amp;`, `&`)
	return s
}

// ValidateAllowedURL implements lt(url, allow) from REF L1008.
func ValidateAllowedURL(rawURL string, allowedDomains []string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", &DomainError{Code: "INVALID_RESPONSE", Message: "invalid URL"}
	}
	if !strings.EqualFold(u.Scheme, "https") || u.User != nil {
		return "", &DomainError{Code: "SECURITY_BLOCKED", Message: "unsupported URL scheme or credentials"}
	}
	port := u.Port()
	if port != "" && port != "443" {
		return "", &DomainError{Code: "SECURITY_BLOCKED", Message: "unsupported port: " + port}
	}
	host := strings.ToLower(u.Hostname())
	allowed := false
	for _, d := range allowedDomains {
		d = strings.ToLower(strings.TrimSpace(d))
		if host == d || strings.HasSuffix(host, "."+d) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", &DomainError{Code: "SECURITY_BLOCKED", Message: "host not in allowlist: " + host}
	}
	return u.String(), nil
}
