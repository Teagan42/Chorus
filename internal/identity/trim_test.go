package identity_test

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
)

// pcmOf lays out silence with one tone between from and to, in samples.
func pcmOf(samples, from, to int, amplitude float64) []byte {
	pcm := make([]byte, 2*samples)
	for i := from; i < to; i++ {
		s := amplitude * math.Sin(2*math.Pi*140*float64(i)/bridge.SampleRate)
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(s)))
	}
	return pcm
}

// A satellite's wake clip is mostly room. The sentence in the middle is what
// the voiceprint is built from, so the silence either side goes.
//
// verifies SPEC §5
func TestTrimmingASatellitesClipToWhatWasSaid(t *testing.T) {
	const samples = 30 * bridge.SampleRate
	from, to := 12*bridge.SampleRate, 16*bridge.SampleRate
	got := identity.TrimSilence(pcmOf(samples, from, to, 8000))

	kept := len(got) / 2
	if kept >= samples {
		t.Fatalf("kept %d samples of %d: the silence was not trimmed", kept, samples)
	}
	// The span plus a pad either side, to the window the scan rounds to.
	wantLow, wantHigh := to-from, to-from+3*bridge.SampleRate/10
	if kept < wantLow || kept > wantHigh {
		t.Errorf("kept %d samples, want between %d and %d", kept, wantLow, wantHigh)
	}
}

// Nothing stands out of an empty room, so there is no span to choose and the
// clip comes back whole for the caller to refuse on length.
//
// verifies SPEC §5
func TestTrimmingKeepsAClipWithNoVoiceWhole(t *testing.T) {
	silence := make([]byte, 2*5*bridge.SampleRate)
	if got := identity.TrimSilence(silence); len(got) != len(silence) {
		t.Errorf("kept %d bytes of %d: silence has no voiced span to trim to", len(got), len(silence))
	}
}

// A clip already all voice keeps every sample: trimming is not a haircut.
//
// verifies SPEC §5
func TestTrimmingKeepsAClipThatIsAllVoice(t *testing.T) {
	const samples = 3 * bridge.SampleRate
	pcm := pcmOf(samples, 0, samples, 8000)
	if got := identity.TrimSilence(pcm); len(got) != len(pcm) {
		t.Errorf("kept %d bytes of %d", len(got), len(pcm))
	}
}

// A clip shorter than one window is returned as it is, rather than divided
// by zero windows.
//
// verifies SPEC §5
func TestTrimmingAClipShorterThanAWindow(t *testing.T) {
	pcm := pcmOf(100, 0, 100, 8000)
	if got := identity.TrimSilence(pcm); len(got) != len(pcm) {
		t.Errorf("kept %d bytes of %d", len(got), len(pcm))
	}
}

// Closest names whoever an embedding is nearest even when no threshold would
// accept it: a reviewer picking enrollment takes has to see that a clip is
// somebody else's before ticking it.
//
// verifies SPEC §5
func TestClosestNamesTheNearestEvenBelowTheThreshold(t *testing.T) {
	ids := &identity.Identities{
		Model: "fake", Dim: 3,
		People: []identity.Person{
			{ID: "alice", Utterances: 3, Centroid: []float32{1, 0, 0}},
			{ID: "bob", Utterances: 3, Centroid: []float32{0, 1, 0}},
		},
	}
	// Mostly bob, but far enough off that Accept refuses it.
	probe := []float32{0.5, 0.65, 0.5}
	id, score, err := ids.Closest(probe)
	if err != nil {
		t.Fatal(err)
	}
	if id != "bob" {
		t.Errorf("closest is %q, want bob", id)
	}
	if score >= identity.DefaultAccept {
		t.Errorf("score %v is not under the accept threshold, so the case is not the one being tested", score)
	}
	// Match, asked the other question, names nobody.
	out, err := ids.Match(probe, identity.Thresholds{})
	if err != nil {
		t.Fatal(err)
	}
	if out.PersonID != "" {
		t.Errorf("Match named %q below the threshold", out.PersonID)
	}
}

// With nobody enrolled there is nobody to be near, and that is not an error.
//
// verifies SPEC §5
func TestClosestWithNobodyEnrolled(t *testing.T) {
	ids := &identity.Identities{Model: "fake", Dim: 3}
	id, _, err := ids.Closest([]float32{1, 0, 0})
	if err != nil || id != "" {
		t.Errorf("Closest = %q, %v; want empty and no error", id, err)
	}
}
