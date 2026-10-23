package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A clip is called under the threshold against the level its own satellite
// is matched at, not the 0.70 default: Alan scoring 0.6 is himself on the
// office satellite at 0.50 and a guest in the kitchen at the house's
// (ADR-0098).
//
// verifies SPEC §9.2
func TestAClipIsJudgedAtItsSatellitesAcceptLevel(t *testing.T) {
	v := newVoicesVisit(t)
	v.s.voices.satAccept = map[string]float64{"office": 0.50}
	takes := v.threeTakes(4, []float32{1, 0, 0, 0})
	if w := enrollPost(t, v.s.routes(), "alan", "Alan", takes); w.Code != http.StatusSeeOther {
		t.Fatalf("enrolling alan answered %d: %s", w.Code, w.Body.String())
	}
	v.emb.anyTake = []float32{0.6, 0.8, 0, 0}
	v.keep(t, "office", "wake/far-field", spokenClip(10, 6, 1))
	v.keep(t, "kitchen", "wake/close-mic", spokenClip(10, 6, 2))

	for sat, under := range map[string]string{"office": "", "kitchen": "under this satellite's 0.70"} {
		rows, err := v.s.deviceClips(context.Background(), sat)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || !strings.Contains(rows[0].Closest, "sounds like alan") {
			t.Fatalf("%s: rows %+v", sat, rows)
		}
		got := rows[0].Closest
		if under == "" && strings.Contains(got, "under") {
			t.Errorf("%s: called under its own 0.50: %q", sat, got)
		}
		if under != "" && !strings.Contains(got, under) {
			t.Errorf("%s: %q, want it to say %q", sat, got, under)
		}
	}

	// The house's override moves the satellites that name no level.
	v.s.voices.house = 0.55
	if a := v.s.voices.acceptFor("kitchen"); a != 0.55 {
		t.Errorf("kitchen accepts at %v, want the house's 0.55", a)
	}
	if a := v.s.voices.acceptFor("office"); a != 0.50 {
		t.Errorf("office accepts at %v, want its own 0.50", a)
	}
}

// The levels are read as chorusd reads them, and refused as it refuses
// them, so the screen never judges against a number the daemon would not
// start with.
func TestAcceptLevelsAreReadAsChorusdReadsThem(t *testing.T) {
	inv := filepath.Join(t.TempDir(), "devices.yaml")
	body := "satellites:\n" +
		"  - {name: office, address: 10.0.0.2:6053, psk: seJlx7BCna54FicYF6Sg4xUB1y+8cUpFHJRm9+eKf2c=, speaker_accept: 0.5}\n" +
		"  - {name: kitchen, address: 10.0.0.3:6053, psk: seJlx7BCna54FicYF6Sg4xUB1y+8cUpFHJRm9+eKf2c=}\n"
	if err := os.WriteFile(inv, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	house, per, err := acceptLevels("0.6", inv)
	if err != nil {
		t.Fatal(err)
	}
	if house != 0.6 || per["office"] != 0.5 || len(per) != 1 {
		t.Errorf("house %v per %v, want 0.6 and only the office at 0.5", house, per)
	}
	if house, per, err := acceptLevels("", ""); err != nil || house != 0 || per != nil {
		t.Errorf("unset: %v %v %v, want nothing and no error", house, per, err)
	}
	for _, bad := range []string{"1", "0", "-0.2", "high"} {
		if _, _, err := acceptLevels(bad, ""); err == nil || !strings.Contains(err.Error(), speakerAcceptEnv) {
			t.Errorf("%s=%q: err = %v, want it refused by name", speakerAcceptEnv, bad, err)
		}
	}
	if _, _, err := acceptLevels("", filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "-devices") {
		t.Errorf("a missing inventory: err = %v, want it refused naming -devices", err)
	}
}
