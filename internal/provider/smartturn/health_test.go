package smartturn_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// The sidecar serving the pinned checkpoint is healthy, and is asked on its
// own /health route.
//
// verifies SPEC §10, §13
func TestASidecarServingThePinnedCheckpointIsHealthy(t *testing.T) {
	rt := &roundTrip{body: `{"status":"ok","model":"smart-turn-v3.2-cpu"}`}
	if err := clientOn(t, rt).Health(context.Background()); err != nil {
		t.Fatalf("health = %v, want nil", err)
	}
	if rt.req.Method != http.MethodGet || rt.req.URL.String() != "http://smartturn.invalid/health" {
		t.Errorf("asked %s %s, want GET the sidecar's /health", rt.req.Method, rt.req.URL)
	}
}

// A sidecar left on the v3.1 checkpoint would have every judgement refused,
// and the endpointer would fall back to silence; readiness says so first
// (ADR-0097).
//
// verifies SPEC §10, §13
func TestASidecarServingAnotherCheckpointIsNotHealthy(t *testing.T) {
	err := clientOn(t, &roundTrip{body: `{"status":"ok","model":"smart-turn-v3.1-cpu"}`}).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), `serves "smart-turn-v3.1-cpu", client is built for "smart-turn-v3.2-cpu"`) {
		t.Errorf("health = %v, want both checkpoints named", err)
	}
	refused := errors.New(`dial tcp 10.0.0.22:8891: connect: connection refused`)
	if err := clientOn(t, &roundTrip{err: refused}).Health(context.Background()); !errors.Is(err, refused) {
		t.Errorf("health = %v, want the dial error", err)
	}
}
