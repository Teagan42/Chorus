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

	// routes overrides the reply per path, for a test that reads several
	// endpoints.
	routes map[string]reply

	hold    bool
	entered chan struct{}
	release chan struct{}

	mu   sync.Mutex
	reqs []seen
}

// reply is one canned response.
type reply struct {
	status int
	body   string
}

func newTransport(status int, body string) *transport {
	return &transport{
		status: status, body: body,
		routes:  map[string]reply{},
		entered: make(chan struct{}, 8), release: make(chan struct{}),
	}
}

func (tr *transport) route(path string, status int, body string) {
	tr.routes[path] = reply{status: status, body: body}
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
	status, resp := tr.status, tr.body
	if routed, ok := tr.routes[r.URL.Path]; ok {
		status, resp = routed.status, routed.body
	}
	if status == 0 {
		status = http.StatusOK
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

// Bodies as Home Assistant 2026.10 really sends them, recorded against a
// running instance (models_test.go). A fake that invents a body proves nothing.
const (
	kitchenOn = `[{"entity_id":"light.kitchen","state":"on","attributes":{"friendly_name":"Kitchen","brightness":128},"last_changed":"2026-10-08T09:00:00.000000+00:00","last_reported":"2026-10-08T09:00:00.000000+00:00","last_updated":"2026-10-08T09:00:00.000000+00:00","context":{"id":"01M4DCGQYW7J356ZYA8T48FZST","parent_id":null,"user_id":"5e72adb7d27b49efae118c633e26dd37"}}]`

	// An entity HA does not have: a JSON message.
	entityNotFound = `{"message":"Entity not found."}`

	// An unknown service, an unknown domain, or rejected service data: all three
	// are aiohttp's default page, with nothing in it to tell them apart.
	badRequest = "400: Bad Request"

	// A rejected token is 401; HA never answers 403 for one.
	unauthorized = "401: Unauthorized"

	// A page of /api/services, as far as the client reads it.
	catalogue = `[{"domain":"homeassistant","services":{"turn_on":{"fields":{},"target":{}}}},{"domain":"light","services":{"turn_on":{"fields":{"brightness_pct":{"selector":{"number":{"min":0,"max":100}}}},"target":{"entity":[{"domain":["light"]}]}},"turn_off":{"fields":{},"target":{"entity":[{"domain":["light"]}]}}}}]`
)
