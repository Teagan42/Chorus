package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/ops"
)

// The ops listener is on by default and on loopback, as the review UI is:
// nothing on it asks who is asking, and its counters say when the house is
// talking. Another address is a choice, and off is a word (ADR-0068).
//
// verifies SPEC §13
func TestTheOpsListenerDefaultsToLoopback(t *testing.T) {
	cfg := configFromEnv(lookup(complete()))
	if cfg.OpsListen != defaultOpsListen || !strings.HasPrefix(defaultOpsListen, "127.0.0.1:") {
		t.Errorf("OpsListen = %q, want the loopback default %q", cfg.OpsListen, defaultOpsListen)
	}

	env := complete()
	env[opsListenEnv] = "off"
	if cfg := configFromEnv(lookup(env)); cfg.validate() != nil || cfg.opsEnabled() {
		t.Errorf("%s=off: enabled %v, validate %v; want off and valid", opsListenEnv, cfg.opsEnabled(), cfg.validate())
	}

	env[opsListenEnv] = "6056"
	if err := configFromEnv(lookup(env)).validate(); err == nil || !strings.Contains(err.Error(), opsListenEnv) {
		t.Errorf("a port with no host validated: %v", err)
	}
}

// The image has no shell and no curl, so the container's healthcheck is the
// binary asking its own /healthz on loopback, wherever the listener binds.
//
// verifies SPEC §13
func TestTheHealthcheckAsksLoopback(t *testing.T) {
	for addr, want := range map[string]string{
		"127.0.0.1:6056": "http://127.0.0.1:6056/healthz",
		":6056":          "http://127.0.0.1:6056/healthz",
		"0.0.0.0:6056":   "http://127.0.0.1:6056/healthz",
		"[::]:6056":      "http://[::1]:6056/healthz",
		"10.0.0.5:6056":  "http://10.0.0.5:6056/healthz",
	} {
		if got := healthURL(addr); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", addr, got, want)
		}
	}

	var asked string
	answer := func(code int) *http.Client {
		return &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			asked = r.URL.String()
			return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
		})}
	}
	if err := healthcheck(context.Background(), answer(http.StatusOK), ":6056"); err != nil || asked != "http://127.0.0.1:6056/healthz" {
		t.Errorf("healthy: %v, asked %q", err, asked)
	}
	if err := healthcheck(context.Background(), answer(http.StatusServiceUnavailable), ":6056"); err == nil {
		t.Error("a 503 passed the healthcheck")
	}
	if err := healthcheck(context.Background(), answer(http.StatusOK), "off"); err == nil {
		t.Error("the healthcheck passed with the listener off")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Readiness names what chorusd already requires at startup as required,
// and the optional sidecars only when configured. Each probe asks the
// dependency's own cheap route, Home Assistant's with the token (SPEC §13).
//
// verifies SPEC §13, §10
func TestReadinessChecksWhatTheHouseConfigured(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		mu.Lock()
		asked = append(asked, r.URL.String()+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Request: r}, nil
	})}
	pgDown := errors.New("connection refused")
	pg := func(context.Context) error { return pgDown }

	full := opsChecks(configFromEnv(lookup(complete())), client, pg)
	var got []string
	for _, c := range full {
		got = append(got, c.Name+"="+strconv.FormatBool(c.Required))
		err := c.Probe(context.Background())
		if c.Name == "postgres" && !errors.Is(err, pgDown) {
			t.Errorf("postgres probe = %v, want the pool's ping", err)
		}
	}
	want := []string{"postgres=true", "stt=true", "tts=true", "llm=true", "speaker_id=false", "smart_turn=false", "home_assistant=false"}
	if !slices.Equal(got, want) {
		t.Errorf("checks = %v, want %v", got, want)
	}
	wantAsked := []string{
		"http://speaches:8000/health ",
		"http://kokoro:8880/health ",
		"http://ollama:11434/api/version ",
		"http://speakerid:8890/health ",
		"http://smartturn:8891/health ",
		"http://homeassistant:8123/api/ Bearer token",
	}
	if !slices.Equal(asked, wantAsked) {
		t.Errorf("probes asked\n%q\nwant\n%q", asked, wantAsked)
	}

	env := complete()
	for _, k := range []string{speakerIDURLEnv, smartTurnURLEnv, "HASS_URL", "HASS_TOKEN"} {
		delete(env, k)
	}
	var names []string
	for _, c := range opsChecks(configFromEnv(lookup(env)), client, pg) {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"postgres", "stt", "tts", "llm"}) {
		t.Errorf("without the optional sidecars, checks = %v", names)
	}
}

// readyz reads the daemon's /readyz with no socket.
func (r *rig) readyz(t *testing.T) ops.Readiness {
	t.Helper()
	rec := httptest.NewRecorder()
	r.dm.ops.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	var doc ops.Readiness
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("/readyz: %v\n%s", err, rec.Body.String())
	}
	return doc
}

func satNamed(doc ops.Readiness, name string) ops.SatelliteState {
	for _, s := range doc.Satellites {
		if s.Name == name {
			return s
		}
	}
	return ops.SatelliteState{}
}

func withOps(d *deps) { d.Ops = &opsDeps{} }

// The kitchen's satellite dials in: /readyz names it connected with the
// frame it last sent, and once its link drops it is disconnected again,
// the office never having connected at all (SPEC §13, §3.2).
//
// verifies SPEC §13, §3.2
func TestAConnectedSatelliteShowsInReadinessUntilItDrops(t *testing.T) {
	r := newRig(t, inventory(), withOps)
	if got := satNamed(r.readyz(t), "kitchen"); got.AudioLink != ops.Disconnected {
		t.Fatalf("before it dialed, kitchen = %+v", got)
	}

	conn := r.ln.dial(t, kitchenIP)
	dev := bridgetest.Connect(conn, 2, as(t, r.dm.byHost[kitchenIP]))
	await(t, "the kitchen connected in /readyz", func() bool {
		return satNamed(r.readyz(t), "kitchen").AudioLink == ops.Connected
	})
	dev.SendWake(t, "hey_eddie")
	await(t, "the kitchen's frame in /readyz", func() bool {
		return satNamed(r.readyz(t), "kitchen").LastFrameAt != nil
	})
	if got := satNamed(r.readyz(t), "kitchen").LastFrameAt; !got.Equal(epoch) {
		t.Errorf("kitchen's last frame at %v, want the daemon's clock, %v", got, epoch)
	}
	if got := satNamed(r.readyz(t), "office"); got.AudioLink != ops.Disconnected || got.Room != "office" {
		t.Errorf("office = %+v, want named and disconnected", got)
	}

	_ = conn.Close()
	await(t, "the kitchen disconnected in /readyz", func() bool {
		return satNamed(r.readyz(t), "kitchen").AudioLink == ops.Disconnected
	})
}

// The native API's state is the dialer's: held once dialed, dropped once
// its connection closes (SPEC §3.1, §13).
//
// verifies SPEC §13, §3.1
func TestTheNativeAPIShowsInReadiness(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer := newDialer()
	conn := newNative()
	dialer.results <- dialResult{conn: conn}
	r := newRig(t, inv, withOps, func(d *deps) { d.Native = dialer })

	await(t, "the native API held in /readyz", func() bool {
		return satNamed(r.readyz(t), "kitchen").NativeAPI == ops.Connected
	})
	_ = conn.Close()
	await(t, "the native API dropped in /readyz", func() bool {
		return satNamed(r.readyz(t), "kitchen").NativeAPI == ops.Disconnected
	})
}

// A session the daemon opens is counted from its journal append: the
// metrics observe the log the daemon writes, not a tally of their own
// (SPEC §8, ADR-0007).
//
// verifies SPEC §8
func TestTheDaemonsJournalFeedsTheMetrics(t *testing.T) {
	r := newRig(t, inventory(), withOps)
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather", alan))
	r.store.awaitKind(t, journal.KindSessionOpened, 1)

	await(t, "a session counted", func() bool {
		rec := httptest.NewRecorder()
		r.dm.ops.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		return strings.Contains(rec.Body.String(), "\nchorus_sessions_opened_total 1\n")
	})
}

// The listener serves over whatever it is handed, and closes with the
// daemon: cancel, and run returns with the endpoint gone (SPEC §13).
//
// verifies SPEC §13
func TestTheOpsListenerServesAndClosesWithTheDaemon(t *testing.T) {
	opsLn := newListener()
	r := newRig(t, inventory(), func(d *deps) { d.Ops = &opsDeps{Listener: opsLn} })
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return opsLn.dial(t, "127.0.0.1"), nil
		},
	}}

	resp, err := client.Get("http://chorusd/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("/healthz = %d", resp.StatusCode)
	}
	resp, err = client.Get("http://chorusd/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	_ = resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/json" {
		t.Errorf("/readyz content type %q", resp.Header.Get("Content-Type"))
	}

	r.cancel()
	if err := r.exit(t); err != nil {
		t.Errorf("run returned %v", err)
	}
	select {
	case <-opsLn.done:
	default:
		t.Error("the ops listener is still open after the daemon stopped")
	}
}
