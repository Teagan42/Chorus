package ops_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/ops"
)

const (
	every   = 15 * time.Second
	timeout = 3 * time.Second
)

// house is the stack a test probes: Postgres and STT required, speaker ID
// optional, as chorusd treats them at startup.
type house struct {
	pg, stt, sid *sidecar
	clk          *clock
	ops          *ops.Ops
	h            http.Handler
}

func newHouse(t *testing.T) *house {
	t.Helper()
	hs := &house{pg: &sidecar{}, stt: &sidecar{}, sid: &sidecar{}, clk: newClock()}
	hs.ops = ops.New(ops.Config{
		Version: "0.17.0", Clock: hs.clk, Timers: hs.clk, Interval: every, Timeout: timeout,
		Checks: []ops.Check{
			{Name: "postgres", Required: true, Probe: hs.pg.probe},
			{Name: "stt", Required: true, Probe: hs.stt.probe},
			{Name: "speaker_id", Probe: hs.sid.probe},
		},
		Satellites: []ops.Satellite{{Name: "kitchen", Room: "kitchen"}, {Name: "office", Room: "office"}},
	})
	hs.h = hs.ops.Handler()
	return hs
}

// run starts the probe loop and stops it when the test ends.
func (hs *house) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hs.ops.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// nextRound waits for the loop to arm its interval, then lets it come due.
func (hs *house) nextRound(t *testing.T) {
	t.Helper()
	await(t, "the next round to be armed", func() bool { return hs.clk.armed(every) > 0 })
	hs.clk.advance(every)
}

func (hs *house) awaitState(t *testing.T, name, want string) {
	t.Helper()
	await(t, name+" "+want, func() bool {
		_, doc := ready(t, hs.h)
		return doc.state(name) == want
	})
}

// The speech recogniser goes down between one round and the next: /readyz
// turns red on the next round, names it, and turns green again on the round
// after it answers. Rounds run on the injected clock only (SPEC §13).
//
// verifies SPEC §13, §7
func TestReadinessFollowsARequiredDependencyDownAndUp(t *testing.T) {
	hs := newHouse(t)
	hs.run(t)
	hs.awaitState(t, "stt", "up")
	if code, doc := ready(t, hs.h); code != http.StatusOK || doc.state("postgres") != "up" {
		t.Fatalf("with everything up /readyz = %d %+v, want 200", code, doc)
	}

	hs.stt.set(errRefused)
	hs.nextRound(t)
	hs.awaitState(t, "stt", "down")
	code, doc := ready(t, hs.h)
	if code != http.StatusServiceUnavailable || doc.Status != "not_ready" {
		t.Errorf("with STT down /readyz = %d %q, want 503 not_ready", code, doc.Status)
	}
	for _, dep := range doc.Dependencies {
		if dep.Name == "stt" && !strings.Contains(dep.Error, "connection refused") {
			t.Errorf("stt's error = %q, want what the probe said", dep.Error)
		}
	}

	hs.stt.set(nil)
	hs.nextRound(t)
	hs.awaitState(t, "stt", "up")
	if code, _ := ready(t, hs.h); code != http.StatusOK {
		t.Errorf("with STT back /readyz = %d, want 200", code)
	}
}

// Speaker ID is optional: chorusd starts without it and the house still
// answers, so its going down degrades readiness without failing it (SPEC §5).
//
// verifies SPEC §13, §5
func TestAnOptionalDependencyDownDegradesWithoutFailing(t *testing.T) {
	hs := newHouse(t)
	hs.sid.set(errRefused)
	hs.run(t)
	hs.awaitState(t, "speaker_id", "down")

	code, doc := ready(t, hs.h)
	if code != http.StatusOK || doc.Status != "degraded" {
		t.Errorf("with speaker ID down /readyz = %d %q, want 200 degraded", code, doc.Status)
	}
}

// A sidecar that never answers is given up on at the probe timeout, on the
// injected clock, and never holds /readyz: a request reads the last round's
// answers, so it is served while the probe hangs (SPEC §13).
//
// verifies SPEC §13
func TestAHungProbeNeverHoldsTheEndpoint(t *testing.T) {
	hs := newHouse(t)
	hs.stt.hang = true
	hs.run(t)
	await(t, "the probe timeout to be armed", func() bool { return hs.clk.armed(timeout) > 0 })

	// Served now, while the probe is still waiting on the sidecar.
	code, doc := ready(t, hs.h)
	if code != http.StatusServiceUnavailable || doc.state("stt") != "unknown" {
		t.Errorf("mid-probe /readyz = %d stt %q, want 503 with stt unknown", code, doc.state("stt"))
	}

	hs.clk.advance(timeout)
	hs.awaitState(t, "stt", "down")
	if n := hs.stt.probes(); n != 1 {
		t.Errorf("the hung sidecar was probed %d times in one round", n)
	}
}

// Nothing is known before the first round, so nothing required is ready.
//
// verifies SPEC §13
func TestNothingIsReadyBeforeTheFirstRound(t *testing.T) {
	hs := newHouse(t)
	code, doc := ready(t, hs.h)
	if code != http.StatusServiceUnavailable || doc.state("postgres") != "unknown" {
		t.Errorf("before any round /readyz = %d postgres %q, want 503 unknown", code, doc.state("postgres"))
	}
	if hs.pg.probes() != 0 {
		t.Error("a request probed a dependency; probes belong to the loop")
	}
}
