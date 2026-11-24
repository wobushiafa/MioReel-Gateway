package upstream

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestQuoteLongJSONIDs(t *testing.T) {
	raw := `{"series_id":7701678491724418073,"id":1234567890123456789,"short":42}`
	got := QuoteLongJSONIDs(raw)
	want := `{"series_id":"7701678491724418073","id":"1234567890123456789","short":42}`
	if got != want {
		t.Fatalf("QuoteLongJSONIDs mismatch:\ngot:  %s\nwant: %s", got, want)
	}
}

func TestClientSecurityAndRedirects(t *testing.T) {
	cli := NewClientWithTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "redirect_post") {
			return &http.Response{
				StatusCode: 302,
				Header:     http.Header{"Location": []string{"https://reading.snssdk.com/other"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		if strings.Contains(r.URL.Path, "redirect_evil") {
			return &http.Response{
				StatusCode: 302,
				Header:     http.Header{"Location": []string{"https://evil.example.com/steal"}},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{"code":0}`)),
		}, nil
	}))

	// Disallowed initial host
	_, err := cli.DoText(context.Background(), "GET", "https://evil.example.com/api", nil, nil, time.Now().Add(5*time.Second))
	var ue *UpstreamError
	if !errors.As(err, &ue) || ue.Code != "SECURITY_BLOCKED" {
		t.Fatalf("expected SECURITY_BLOCKED for bad host, got %v", err)
	}

	// POST 302 redirect must fail with SECURITY_BLOCKED
	_, err = cli.DoText(context.Background(), "POST", "https://reading.snssdk.com/redirect_post", nil, []byte("{}"), time.Now().Add(5*time.Second))
	if !errors.As(err, &ue) || ue.Code != "SECURITY_BLOCKED" {
		t.Fatalf("expected SECURITY_BLOCKED on POST redirect, got %v", err)
	}

	// GET redirect to evil host must fail with SECURITY_BLOCKED
	_, err = cli.DoText(context.Background(), "GET", "https://reading.snssdk.com/redirect_evil", nil, nil, time.Now().Add(5*time.Second))
	if !errors.As(err, &ue) || ue.Code != "SECURITY_BLOCKED" {
		t.Fatalf("expected SECURITY_BLOCKED on evil redirect, got %v", err)
	}
}
