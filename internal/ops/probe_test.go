package ops_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/ops"
)

// roundTrip answers a request without a socket.
type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func answering(status int, seen *http.Request) *http.Client {
	return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		*seen = *r
		return &http.Response{StatusCode: status, Status: http.StatusText(status), Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)), Request: r}, nil
	})}
}

// A sidecar is up when its health route answers 2xx. Anything else is down,
// named with what it answered, and a credential the check carries is sent
// as a header and never quoted back (SPEC §13).
//
// verifies SPEC §13
func TestAnHTTPProbeIsUpOnlyOnSuccess(t *testing.T) {
	var seen http.Request
	ok := ops.HTTPProbe(answering(http.StatusOK, &seen), "http://speaches:8000/health", nil)
	if err := ok(context.Background()); err != nil {
		t.Errorf("200 = %v, want up", err)
	}
	if seen.Method != http.MethodGet || seen.URL.String() != "http://speaches:8000/health" {
		t.Errorf("probed %s %s", seen.Method, seen.URL)
	}

	hdr := http.Header{"Authorization": {"Bearer hass-secret"}}
	down := ops.HTTPProbe(answering(http.StatusUnauthorized, &seen), "http://homeassistant.local:8123/api/", hdr)
	err := down(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("401 = %v, want down naming the status", err)
	}
	if strings.Contains(err.Error(), "hass-secret") {
		t.Errorf("the error quotes the token: %v", err)
	}
	if seen.Header.Get("Authorization") != "Bearer hass-secret" {
		t.Error("the check's header was not sent")
	}

	refused := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) {
		return nil, errRefused
	})}
	if err := ops.HTTPProbe(refused, "http://user:pw@kokoro:8880/health", nil)(context.Background()); !errors.Is(err, errRefused) || strings.Contains(err.Error(), "pw@") {
		t.Errorf("refused = %v, want the transport's error with the URL redacted", err)
	}
}
