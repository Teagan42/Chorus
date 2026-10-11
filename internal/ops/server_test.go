package ops_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// /healthz says the process is up and its probe loop is going round; it
// calls nothing. Before the first round it is starting, and once the loop
// has missed three rounds on the injected clock it is not alive (SPEC §13).
//
// verifies SPEC §13
func TestHealthIsTheLoopGoingRound(t *testing.T) {
	hs := newHouse(t)
	if rec := get(t, hs.h, "/healthz"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "starting") {
		t.Errorf("before the loop /healthz = %d %s, want 503 starting", rec.Code, rec.Body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		hs.ops.Run(ctx)
	}()
	await(t, "the first round", func() bool { return get(t, hs.h, "/healthz").Code == http.StatusOK })
	hs.awaitState(t, "postgres", "up")
	hs.awaitState(t, "stt", "up")
	hs.awaitState(t, "speaker_id", "up")
	probed := hs.pg.probes() + hs.stt.probes() + hs.sid.probes()
	for range 5 {
		get(t, hs.h, "/healthz")
	}
	if n := hs.pg.probes() + hs.stt.probes() + hs.sid.probes(); n != probed {
		t.Errorf("/healthz probed dependencies: %d probes, want %d", n, probed)
	}

	// The loop stops going round while the clock goes on.
	cancel()
	<-done
	hs.clk.advance(3*every + time.Second)
	if rec := get(t, hs.h, "/healthz"); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("with the loop stopped /healthz = %d %s, want 503", rec.Code, rec.Body)
	}
}

// Each satellite in the inventory is named in /readyz: whether its audio
// link is up, whether its native API is held, and when a frame last came in.
// A satellite off the Wi-Fi degrades readiness without failing it: chorusd
// restarting would not bring it back (SPEC §13, §3.2).
//
// verifies SPEC §13, §3.2
func TestEachSatelliteIsNamedWithItsLinks(t *testing.T) {
	hs := newHouse(t)
	hs.run(t)
	hs.awaitState(t, "speaker_id", "up")

	sats := hs.ops.Satellites()
	sats.Linked("kitchen", true)
	sats.Native("kitchen", true)
	sats.Native("office", true)
	hs.clk.advance(2 * time.Second)
	sats.Frame("kitchen")
	heardAt := hs.clk.Now()

	code, doc := ready(t, hs.h)
	if code != http.StatusOK || doc.Status != "degraded" {
		t.Errorf("with the office's audio link down /readyz = %d %q, want 200 degraded", code, doc.Status)
	}
	if len(doc.Satellites) != 2 {
		t.Fatalf("satellites = %+v, want the inventory's two", doc.Satellites)
	}
	kitchen, office := doc.Satellites[0], doc.Satellites[1]
	if kitchen.Name != "kitchen" || kitchen.Room != "kitchen" || kitchen.AudioLink != "connected" || kitchen.NativeAPI != "connected" {
		t.Errorf("kitchen = %+v, want both links connected", kitchen)
	}
	if kitchen.LastFrameAt == nil || !kitchen.LastFrameAt.Equal(heardAt) {
		t.Errorf("kitchen's last frame = %v, want %v", kitchen.LastFrameAt, heardAt)
	}
	if office.AudioLink != "disconnected" || office.NativeAPI != "connected" || office.LastFrameAt != nil {
		t.Errorf("office = %+v, want the audio link down and no frame seen", office)
	}
	if got := sample(t, hs.h, "chorus_satellites_connected"); got != 1 {
		t.Errorf("chorus_satellites_connected = %v, want 1", got)
	}

	sats.Linked("office", true)
	if code, doc := ready(t, hs.h); code != http.StatusOK || doc.Status != "ready" {
		t.Errorf("with every link up /readyz = %d %q, want 200 ready", code, doc.Status)
	}
	sats.Linked("kitchen", false)
	if _, doc := ready(t, hs.h); doc.Satellites[0].AudioLink != "disconnected" || doc.Satellites[0].LastFrameAt == nil {
		t.Errorf("kitchen after its link dropped = %+v, want disconnected, its last frame kept", doc.Satellites[0])
	}
	if got := sample(t, hs.h, `chorus_satellite_audio_link_connected{satellite="kitchen"}`); got != 0 {
		t.Errorf("kitchen's audio link gauge = %v, want 0", got)
	}
	if got := sample(t, hs.h, `chorus_dependency_up{dependency="stt",required="true"}`); got != 1 {
		t.Errorf("stt's dependency gauge = %v, want 1", got)
	}
}

// A name the inventory does not hold changes nothing, and an endpoint not
// served is a 404, not the readiness document.
//
// verifies SPEC §13
func TestOnlyTheInventoryAndTheThreeEndpointsAreServed(t *testing.T) {
	hs := newHouse(t)
	hs.ops.Satellites().Linked("garage", true)
	if _, doc := ready(t, hs.h); len(doc.Satellites) != 2 {
		t.Errorf("satellites = %+v, want the inventory's two", doc.Satellites)
	}
	if rec := get(t, hs.h, "/debug/pprof/"); rec.Code != http.StatusNotFound {
		t.Errorf("/debug/pprof/ = %d, want 404", rec.Code)
	}
	if ct := get(t, hs.h, "/readyz").Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("/readyz content type = %q", ct)
	}
	if ct := get(t, hs.h, "/metrics").Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("/metrics content type = %q, want Prometheus text", ct)
	}
}
