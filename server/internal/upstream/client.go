package upstream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mioreel-gateway/server/internal/resolver"
)

// UpstreamAPIDomains are the permitted upstream API domain suffixes (docs/03-algorithm-spec.md §8.2).
var UpstreamAPIDomains = []string{"fqnovel.com", "snssdk.com"}

var (
	largeBodyURLPattern = regexp.MustCompile(`^https://reading\.snssdk\.com/reading/bookapi/(search/tab|bookmall/(tab|cell/change))/v\?|^https://reading\.snssdk\.com/novel/player/video_detail/v1/\?`)
	// Lt() regex from REF L814: quote bare >=16-digit integers on id / *_id fields
	longIDJSONPattern = regexp.MustCompile(`("(?:[A-Za-z0-9_]*_id|id)"\s*:\s*)(-?\d{16,})(\s*[,}\]])`)
)

// UpstreamError captures an upstream transport or protocol failure with its canonical code.
type UpstreamError struct {
	Code            string
	Message         string
	RoutesExhausted bool
	Err             error
}

func (e *UpstreamError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

func (e *UpstreamError) Unwrap() error { return e.Err }

// IsRecoverableModelError matches REF L1048 recoverable codes for video_model route fallback.
func IsRecoverableModelError(err error) bool {
	var ue *UpstreamError
	if !errors.As(err, &ue) {
		return false
	}
	switch ue.Code {
	case "MISSING_MODEL", "EMPTY_RESPONSE", "RESPONSE_NOT_UTF8", "INVALID_RESPONSE",
		"HTTP_0", "HTTP_404", "HTTP_405", "HTTP_408",
		"HTTP_500", "HTTP_502", "HTTP_503", "HTTP_504",
		"ETIMEDOUT", "DNS", "TIMEOUT", "CONNECTION":
		return true
	default:
		return false
	}
}

// Client implements Pe() transport rules: manual redirects, host allowlists, body size caps, and error classification.
type Client struct {
	httpCli *http.Client
}

func NewClient() *Client {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   8 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}
	return NewClientWithTransport(transport)
}

// NewClientWithTransport creates an upstream Client using a custom RoundTripper (useful for tests).
func NewClientWithTransport(rt http.RoundTripper) *Client {
	return &Client{
		httpCli: &http.Client{
			Transport: rt,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// QuoteLongJSONIDs implements Lt() string pre-processing (REF L814) so 16+ digit IDs are safely quoted.
func QuoteLongJSONIDs(raw string) string {
	return longIDJSONPattern.ReplaceAllString(raw, `$1"$2"$3`)
}

// DoText executes an upstream API call adhering to Pe() constraints (REF L1008).
func (c *Client) DoText(ctx context.Context, method, rawURL string, headers map[string]string, body []byte, deadline time.Time) (string, error) {
	if method == "" {
		method = http.MethodGet
	}
	currentURL, err := resolver.ValidateAllowedURL(rawURL, UpstreamAPIDomains)
	if err != nil {
		if de, ok := err.(*resolver.DomainError); ok {
			return "", &UpstreamError{Code: de.Code, Message: de.Message, Err: err}
		}
		return "", &UpstreamError{Code: "INVALID_RESPONSE", Message: err.Error(), Err: err}
	}

	for hop := 0; hop <= 3; hop++ {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return "", &UpstreamError{Code: "ETIMEDOUT", Message: "upstream deadline exceeded", Err: ctx.Err()}
			}
			return "", &UpstreamError{Code: "CANCELLED", Message: "request cancelled", Err: ctx.Err()}
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", &UpstreamError{Code: "ETIMEDOUT", Message: "upstream timeout", Err: context.DeadlineExceeded}
		}
		if remaining > 12*time.Second {
			remaining = 12 * time.Second
		}

		reqCtx, cancel := context.WithTimeout(ctx, remaining)
		var bodyReader io.Reader
		if len(body) > 0 && method != http.MethodGet && method != http.MethodHead {
			bodyReader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(reqCtx, method, currentURL, bodyReader)
		if err != nil {
			cancel()
			return "", &UpstreamError{Code: "INVALID_RESPONSE", Message: err.Error(), Err: err}
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := c.httpCli.Do(req)
		if err != nil {
			cancel()
			return "", classifyNetErr(ctx, err)
		}

		if resp.StatusCode == 301 || resp.StatusCode == 302 || resp.StatusCode == 303 || resp.StatusCode == 307 || resp.StatusCode == 308 {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			cancel()
			if method == http.MethodPost || loc == "" {
				return "", &UpstreamError{Code: "SECURITY_BLOCKED", Message: "disallowed upstream redirect"}
			}
			baseU, _ := url.Parse(currentURL)
			relU, err := url.Parse(loc)
			if err != nil {
				return "", &UpstreamError{Code: "SECURITY_BLOCKED", Message: "invalid redirect location"}
			}
			resolved := baseU.ResolveReference(relU).String()
			nextURL, err := resolver.ValidateAllowedURL(resolved, UpstreamAPIDomains)
			if err != nil {
				if de, ok := err.(*resolver.DomainError); ok {
					return "", &UpstreamError{Code: de.Code, Message: de.Message, Err: err}
				}
				return "", &UpstreamError{Code: "SECURITY_BLOCKED", Message: err.Error(), Err: err}
			}
			currentURL = nextURL
			continue
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			cancel()
			return "", &UpstreamError{
				Code:    "HTTP_" + strconv.Itoa(resp.StatusCode),
				Message: fmt.Sprintf("upstream HTTP %d", resp.StatusCode),
			}
		}

		maxBytes := int64(2 * 1024 * 1024)
		if largeBodyURLPattern.MatchString(currentURL) {
			maxBytes = 12 * 1024 * 1024
		}
		rawBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		_ = resp.Body.Close()
		cancel()
		if readErr != nil {
			return "", classifyNetErr(ctx, readErr)
		}
		if int64(len(rawBytes)) > maxBytes {
			return "", &UpstreamError{Code: "INVALID_RESPONSE", Message: "upstream response exceeded size cap"}
		}
		if !utf8.Valid(rawBytes) {
			return "", &UpstreamError{Code: "RESPONSE_NOT_UTF8", Message: "upstream response is not valid UTF-8"}
		}
		text := string(rawBytes)
		if strings.TrimSpace(text) == "" {
			return "", &UpstreamError{Code: "EMPTY_RESPONSE", Message: "upstream returned empty body"}
		}
		return QuoteLongJSONIDs(text), nil
	}

	return "", &UpstreamError{Code: "SECURITY_BLOCKED", Message: "too many redirects"}
}

// FetchWebHTML fetches public hongguoduanju.com web fallback pages (max 4MB).
func (c *Client) FetchWebHTML(ctx context.Context, rawURL string, headers map[string]string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.httpCli.Do(req)
	if err != nil {
		return "", classifyNetErr(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", &UpstreamError{Code: "HTTP_" + strconv.Itoa(resp.StatusCode), Message: "web fallback HTTP error"}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", classifyNetErr(ctx, err)
	}
	return string(raw), nil
}

// HeadMedia probes a direct CDN URL via HEAD without Referer (docs/03-algorithm-spec.md §13.2).
// Returns (statusCode, contentType, contentLength, error).
func (c *Client) HeadMedia(ctx context.Context, mediaURL string, headers map[string]string, timeout time.Duration) (int, string, int64, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodHead, mediaURL, nil)
	if err != nil {
		return 0, "", 0, err
	}
	for k, v := range headers {
		if strings.EqualFold(k, "referer") {
			continue
		}
		req.Header.Set(k, v)
	}

	// Media CDN may issue 302 redirects across CDN nodes; follow redirects safely with standard client for HEAD.
	cdnCli := &http.Client{
		Transport: c.httpCli.Transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many media redirects")
			}
			// Never leak Referer on redirects
			req.Header.Del("Referer")
			return nil
		},
	}
	resp, err := cdnCli.Do(req)
	if err != nil {
		return 0, "", 0, classifyNetErr(ctx, err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Content-Type"), resp.ContentLength, nil
}

func classifyNetErr(parentCtx context.Context, err error) *UpstreamError {
	if parentCtx != nil && errors.Is(parentCtx.Err(), context.Canceled) {
		return &UpstreamError{Code: "CANCELLED", Message: "cancelled", Err: err}
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return &UpstreamError{Code: "ETIMEDOUT", Message: err.Error(), Err: err}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return &UpstreamError{Code: "DNS", Message: err.Error(), Err: err}
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return &UpstreamError{Code: "CONNECTION", Message: err.Error(), Err: err}
	}
	return &UpstreamError{Code: "HTTP_0", Message: err.Error(), Err: err}
}
