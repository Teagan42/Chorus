package listen_test

import (
	"testing"

	"github.com/teaganglenn/chorus/internal/listen"
)

// An utterance starts on the first chunk at or above the threshold and ends
// once that much quiet has followed it, counted in audio rather than on a
// clock. The chunk that crosses each boundary belongs to the utterance.
//
// verifies SPEC §4.5
func TestEnergyEndpointerCutsOnTrailingSilence(t *testing.T) {
	ep := &listen.Energy{Threshold: 0.1, Silence: 3 * chunkBytes}

	for range 3 {
		if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
			t.Fatalf("silence before speech = %v, want Continue", got)
		}
	}
	if got := ep.Feed(voice(8000, chunkBytes)); got != listen.Start {
		t.Fatalf("first voiced chunk = %v, want Start", got)
	}
	if got := ep.Feed(voice(8000, chunkBytes)); got != listen.Continue {
		t.Fatalf("second voiced chunk = %v, want Continue", got)
	}
	// Two quiet chunks are short of the bound; the third reaches it.
	for i := range 2 {
		if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
			t.Fatalf("quiet chunk %d = %v, want Continue", i, got)
		}
	}
	if got := ep.Feed(quiet(chunkBytes)); got != listen.End {
		t.Fatalf("quiet reaching the bound = %v, want End", got)
	}
	if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
		t.Fatalf("silence after the end = %v, want Continue", got)
	}
	if got := ep.Feed(voice(8000, chunkBytes)); got != listen.Start {
		t.Fatalf("speech after the end = %v, want a new Start", got)
	}
}

// "turn off the... uh... kitchen lights": a pause shorter than the bound is
// inside the utterance, and speech after it starts the count over.
//
// verifies SPEC §4.5
func TestEnergyEndpointerForgivesAPauseShorterThanTheBound(t *testing.T) {
	ep := &listen.Energy{Threshold: 0.1, Silence: 3 * chunkBytes}
	ep.Feed(voice(8000, chunkBytes))
	ep.Feed(quiet(chunkBytes))
	ep.Feed(quiet(chunkBytes))
	ep.Feed(voice(8000, chunkBytes))
	// Two quiet chunks again: with the count carried over this would end.
	ep.Feed(quiet(chunkBytes))
	if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
		t.Fatalf("after a pause and more speech = %v, want Continue", got)
	}
	if got := ep.Feed(quiet(chunkBytes)); got != listen.End {
		t.Fatalf("the bound after the second pause = %v, want End", got)
	}
}

// Reset forgets an utterance in progress, which is what a mute does to it.
func TestEnergyEndpointerResetForgetsTheUtterance(t *testing.T) {
	ep := &listen.Energy{Threshold: 0.1, Silence: 2 * chunkBytes}
	ep.Feed(voice(8000, chunkBytes))
	ep.Reset()
	if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
		t.Fatalf("quiet after reset = %v, want Continue", got)
	}
	if got := ep.Feed(quiet(chunkBytes)); got != listen.Continue {
		t.Fatalf("a reset utterance ended on silence: %v", got)
	}
	if got := ep.Feed(voice(8000, chunkBytes)); got != listen.Start {
		t.Fatalf("speech after reset = %v, want Start", got)
	}
}

// The defaults are what the listener runs with; both are stated in bytes of
// device audio so a test can drive them without a clock.
func TestEnergyDefaults(t *testing.T) {
	ep := listen.NewEnergy()
	if ep.Threshold != listen.DefaultSpeechEnergy || ep.Silence != listen.DefaultSilence {
		t.Errorf("NewEnergy() = %+v, want the package defaults", ep)
	}
	if listen.DefaultSilence%2 != 0 {
		t.Errorf("DefaultSilence = %d bytes is not whole samples", listen.DefaultSilence)
	}
}

// RMS is a fraction of full scale, so Candidate.Energy and Gate.MinEnergy
// share a unit.
//
// verifies SPEC §4.3
func TestRMSIsAFractionOfFullScale(t *testing.T) {
	if got := listen.RMS(quiet(chunkBytes)); got != 0 {
		t.Errorf("RMS(silence) = %v, want 0", got)
	}
	if got := listen.RMS(voice(32767, chunkBytes)); got < 0.9999 || got > 1 {
		t.Errorf("RMS(full scale) = %v, want 1", got)
	}
	if got := listen.RMS(voice(-16384, chunkBytes)); got < 0.49 || got > 0.51 {
		t.Errorf("RMS(half scale, negative) = %v, want 0.5", got)
	}
	if got := listen.RMS(nil); got != 0 {
		t.Errorf("RMS(nil) = %v, want 0", got)
	}
	// A torn trailing byte is ignored rather than read as a sample.
	if got := listen.RMS(voice(32767, chunkBytes)[:chunkBytes-1]); got < 0.9999 || got > 1 {
		t.Errorf("RMS(odd length) = %v, want 1", got)
	}
}
