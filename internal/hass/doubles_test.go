package hass_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/hass"
)

// patience bounds a wait that only expires when the implementation is wrong.
const patience = 2 * time.Second

const token = "eyJ0eXAiOiJKV1QiLCJhbGciOiJIUzI1NiJ9.secret-token-value"

// seen is one request as the transport saw it.
type seen struct {
	method string
	path   string
	auth   string
	accept string
	ctype  string
	body   string
}

// transport serves a canned response in-process and keeps every request. No
// listener: hermetic means hermetic (CONTRIBUTING §1). With hold set it parks
// each call until released, which is how a test positions speech and a
// barge-in while the service call is in flight.
type transport struct {
	status int
	body   string
	err    error

	// routes overrides body per path, for a test that reads several endpoints.
	routes map[string]string

	hold    bool
	entered chan struct{}
	release chan struct{}

	mu   sync.Mutex
	reqs []seen
}

func newTransport(status int, body string) *transport {
	return &transport{
		status: status, body: body,
		entered: make(chan struct{}, 8), release: make(chan struct{}),
	}
}

func (tr *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	var body []byte
	if r.Body != nil {
		body, _ = io.ReadAll(r.Body)
	}
	tr.mu.Lock()
	tr.reqs = append(tr.reqs, seen{
		method: r.Method, path: r.URL.Path,
		auth: r.Header.Get("Authorization"), accept: r.Header.Get("Accept"),
		ctype: r.Header.Get("Content-Type"), body: string(body),
	})
	tr.mu.Unlock()

	if tr.hold {
		tr.entered <- struct{}{}
		select {
		case <-tr.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	if tr.err != nil {
		return nil, tr.err
	}
	status := tr.status
	if status == 0 {
		status = http.StatusOK
	}
	resp := tr.body
	if routed, ok := tr.routes[r.URL.Path]; ok {
		resp = routed
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(resp)),
		Header:     http.Header{},
		Request:    r,
	}, nil
}

// enter waits for a held call to reach the wire.
func (tr *transport) enter(t *testing.T) {
	t.Helper()
	select {
	case <-tr.entered:
	case <-time.After(patience):
		t.Fatal("no request reached the transport")
	}
}

func (tr *transport) last(t *testing.T) seen {
	t.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.reqs) == 0 {
		t.Fatal("no request was made")
	}
	return tr.reqs[len(tr.reqs)-1]
}

func (tr *transport) count() int {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return len(tr.reqs)
}

// clientOn wires a Client to a transport.
func clientOn(t *testing.T, tr *transport) *hass.Client {
	t.Helper()
	c, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123",
		Token:   token,
		HTTP:    &http.Client{Transport: tr},
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

const kitchenOn = `[{"entity_id":"light.kitchen","state":"on","attributes":{"friendly_name":"Kitchen","brightness":128},"last_changed":"2026-10-08T09:00:00+00:00","last_updated":"2026-10-08T09:00:00+00:00"}]`
