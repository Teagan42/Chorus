package speakerid_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/speakerid"
)

// titanet is the client chorusd builds: the defaults, nothing overridden.
func titanet(t *testing.T, rt *roundTrip) *speakerid.Client {
	t.Helper()
	c, err := speakerid.New(speakerid.Config{BaseURL: "http://speakerid:8890/", HTTP: &http.Client{Transport: rt}})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

// The sidecar the household runs, loaded and serving TitaNet-L at 192
// dims, is healthy, and is asked on its own /health route.
//
// verifies SPEC §10, §13
func TestASidecarServingTitaNetIsHealthy(t *testing.T) {
	rt := &roundTrip{body: `{"status":"ok","model":"nemo_en_titanet_large","dim":192}`}
	if err := titanet(t, rt).Health(context.Background()); err != nil {
		t.Fatalf("health = %v, want nil", err)
	}
	if rt.req.Method != http.MethodGet || rt.req.URL.String() != "http://speakerid:8890/health" {
		t.Errorf("asked %s %s, want GET the sidecar's /health", rt.req.Method, rt.req.URL)
	}
}

// A sidecar rebuilt on ECAPA-TDNN answers /health happily and would have
// every embed refused: every spoken wake rejected, and nothing at startup
// to say why. Readiness names both models instead (ADR-0097).
//
// verifies SPEC §10, §13
func TestASidecarServingAnotherModelIsNotHealthy(t *testing.T) {
	for name, c := range map[string]struct{ body, want string }{
		"another model": {`{"status":"ok","model":"ecapa_tdnn","dim":192}`, `serves "ecapa_tdnn", client is built for "nemo_en_titanet_large"`},
		"another width": {`{"status":"ok","model":"nemo_en_titanet_large","dim":256}`, "embeds at 256 dims, client is built for 192"},
		"not json":      {`<html>502 bad gateway</html>`, "decode reply"},
	} {
		t.Run(name, func(t *testing.T) {
			err := titanet(t, &roundTrip{body: c.body}).Health(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("health = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// Down is down, and the error says which way.
//
// verifies SPEC §13
func TestASidecarThatIsDownIsNotHealthy(t *testing.T) {
	refused := errors.New(`dial tcp 10.0.0.22:8890: connect: connection refused`)
	if err := titanet(t, &roundTrip{err: refused}).Health(context.Background()); !errors.Is(err, refused) {
		t.Errorf("health = %v, want the dial error", err)
	}
	err := titanet(t, &roundTrip{status: http.StatusServiceUnavailable, body: "model still loading"}).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "model still loading") {
		t.Errorf("health = %v, want the 503 and its body", err)
	}
}
