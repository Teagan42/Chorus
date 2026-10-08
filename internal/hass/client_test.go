package hass_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
)

// HA takes the target as top-level service data, beside the service's own
// fields, and answers with the states the call changed.
func TestCallServiceSendsTheTargetAsServiceData(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	c := clientOn(t, tr)

	changed, err := c.CallService(context.Background(), "light", "turn_on", map[string]any{
		"entity_id": "light.kitchen", "brightness_pct": 50,
	})
	if err != nil {
		t.Fatalf("call service: %v", err)
	}
	if len(changed) != 1 || changed[0].EntityID != "light.kitchen" || changed[0].State != "on" {
		t.Errorf("changed = %+v, want light.kitchen on", changed)
	}

	req := tr.last(t)
	switch {
	case req.method != http.MethodPost:
		t.Errorf("method = %s, want POST", req.method)
	case req.path != "/api/services/light/turn_on":
		t.Errorf("path = %q", req.path)
	case req.auth != "Bearer "+token:
		t.Errorf("authorization = %q, want the bearer token", req.auth)
	case req.ctype != "application/json":
		t.Errorf("content-type = %q", req.ctype)
	case req.body != `{"brightness_pct":50,"entity_id":"light.kitchen"}`:
		t.Errorf("body = %s", req.body)
	}
}

func TestEntityStateReadsOneEntity(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn[1:len(kitchenOn)-1])
	c := clientOn(t, tr)

	st, err := c.EntityState(context.Background(), "light.kitchen")
	if err != nil {
		t.Fatalf("entity state: %v", err)
	}
	if st.State != "on" || st.Attributes["friendly_name"] != "Kitchen" {
		t.Errorf("state = %+v", st)
	}
	req := tr.last(t)
	if req.method != http.MethodGet || req.path != "/api/states/light.kitchen" {
		t.Errorf("request = %s %s", req.method, req.path)
	}
	if req.auth != "Bearer "+token {
		t.Errorf("authorization = %q", req.auth)
	}
}

// A 404 here means one thing, and the model needs to hear it as that thing
// rather than as a status code (SPEC §7).
func TestAnUnknownEntityIsNamed(t *testing.T) {
	c := clientOn(t, newTransport(http.StatusNotFound, `{"message":"Entity not found."}`))
	_, err := c.EntityState(context.Background(), "light.nope")
	if !errors.Is(err, hass.ErrUnknownEntity) {
		t.Fatalf("err = %v, want ErrUnknownEntity", err)
	}
	if !strings.Contains(err.Error(), "light.nope") {
		t.Errorf("err = %v, want the entity named", err)
	}
}

// The token is the one thing an error must never carry, even when a server
// echoes the request back.
func TestARejectedTokenIsNeverQuoted(t *testing.T) {
	c := clientOn(t, newTransport(http.StatusUnauthorized, `{"message":"bad token `+token+`"}`))
	_, err := c.States(context.Background())
	if !errors.Is(err, hass.ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
	if strings.Contains(err.Error(), token) {
		t.Errorf("err quotes the token: %v", err)
	}
}

func TestAnEchoedTokenIsRedactedFromOtherErrors(t *testing.T) {
	c := clientOn(t, newTransport(http.StatusBadGateway, "proxy saw Authorization: Bearer "+token))
	_, err := c.States(context.Background())
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("err = %v, want the token redacted", err)
	}
}

// Only the body separates an unknown service from bad service data: both are
// 400. Quoted, but bounded, so a proxy's HTML page cannot become a log line.
func TestTheErrorBodyIsQuotedButBounded(t *testing.T) {
	c := clientOn(t, newTransport(http.StatusBadRequest, `{"message":"Service light.explode not found."}`))
	_, err := c.CallService(context.Background(), "light", "explode", nil)
	if err == nil || !strings.Contains(err.Error(), "Service light.explode not found") {
		t.Errorf("err = %v, want the endpoint's explanation", err)
	}

	huge := strings.Repeat("x", 64<<10)
	c = clientOn(t, newTransport(http.StatusInternalServerError, huge))
	_, err = c.CallService(context.Background(), "light", "turn_on", nil)
	if err == nil {
		t.Fatal("a 500 must fail the call")
	}
	if len(err.Error()) > 2<<10 {
		t.Errorf("err is %d bytes; the body was not bounded", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err = %v, want the status", err)
	}
}

func TestADeadEndpointFailsTheCall(t *testing.T) {
	tr := newTransport(0, "")
	tr.err = errors.New("dial tcp: connection refused")
	c := clientOn(t, tr)
	if _, err := c.States(context.Background()); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("err = %v", err)
	}
}

// A cancelled tool child must let go of the wire, and report that it did as
// cancellation rather than as a failure (SPEC §4.4).
func TestACancelledContextAbandonsTheCall(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	tr.hold = true
	c := clientOn(t, tr)

	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := c.States(ctx)
		errc <- err
	}()
	tr.enter(t)
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestFromEnvReadsTheDocumentedNames(t *testing.T) {
	env := map[string]string{"HASS_URL": "http://homeassistant.invalid:8123", "HASS_TOKEN": token}
	cfg := hass.FromEnv(func(k string) string { return env[k] })
	if cfg.BaseURL != env["HASS_URL"] || cfg.Token != token {
		t.Errorf("config = %+v", cfg)
	}
	if _, err := hass.New(cfg); err != nil {
		t.Errorf("new from env: %v", err)
	}
}

func TestNewRejectsIncompleteWiring(t *testing.T) {
	for name, cfg := range map[string]hass.Config{
		"no url":   {Token: token},
		"no token": {BaseURL: "http://homeassistant.invalid:8123"},
	} {
		if _, err := hass.New(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
