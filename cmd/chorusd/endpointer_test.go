package main

import (
	"os"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/listen"
)

// A satellite that set its own speech gate gets it, whether or not a judge
// is configured: the gate is what finds the pause the judge is asked about,
// so a wrong one stops semantic endpointing before it starts (ADR-0083).
//
// verifies SPEC §4.5
func TestASatellitesSpeechGateReachesItsEndpointer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		judge listen.Judge
	}{
		{"with a judge", &turnJudge{}},
		{"with no judge", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := providers{judge: tc.judge}
			ep := p.endpointer(0.05)
			switch got := ep.(type) {
			case *listen.Semantic:
				if got.Threshold != 0.05 {
					t.Errorf("semantic threshold is %v, want 0.05", got.Threshold)
				}
			case *listen.Energy:
				if got.Threshold != 0.05 {
					t.Errorf("energy threshold is %v, want 0.05", got.Threshold)
				}
			default:
				t.Fatalf("endpointer is %T, which carries no threshold", ep)
			}
		})
	}
}

// A satellite that set none keeps the built-in default, so one room's
// measurement is not imposed on a board nobody measured.
//
// verifies SPEC §4.5
func TestNoSpeechGateKeepsTheDefault(t *testing.T) {
	if ep := (providers{}).endpointer(0); ep != nil {
		t.Errorf("endpointer is %T with no judge and no gate, want nil so the listener defaults", ep)
	}
	s, ok := (providers{judge: &turnJudge{}}).endpointer(0).(*listen.Semantic)
	if !ok {
		t.Fatal("a configured judge did not produce a semantic endpointer")
	}
	if s.Threshold != listen.DefaultSpeechEnergy {
		t.Errorf("semantic threshold is %v, want the default %v", s.Threshold, listen.DefaultSpeechEnergy)
	}
}

// The satellite's speech gate also reaches the sustain floor, which counts
// windows at that level. Passed only to the endpointer, the floor would
// count windows at the built-in default -- under a room that rides its own
// gain -- and a mouse click would clear 200 ms of it without trouble
// (ADR-0083, ADR-0086).
//
// verifies SPEC §4.5
func TestTheSpeechGateAlsoReachesTheSustainFloor(t *testing.T) {
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	// The listener is opened in one place; both fields must read the
	// satellite's own gate rather than one of them defaulting silently.
	open := string(src)
	i := strings.Index(open, "listen.Open(linkCtx, listen.Config{")
	if i < 0 {
		t.Fatal("listen.Open call not found; this test guards its arguments")
	}
	call := open[i:]
	if end := strings.Index(call, "})"); end > 0 {
		call = call[:end]
	}
	for _, want := range []string{"Endpointer: d.endpointer(sat.SpeechEnergy)", "SpeechEnergy: sat.SpeechEnergy"} {
		if !strings.Contains(call, want) {
			t.Errorf("listen.Config is missing %q, so it would take the built-in default", want)
		}
	}
}
