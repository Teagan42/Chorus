package listen_test

import (
	"encoding/binary"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/listen"
)

// click is a loud impulse in otherwise quiet room: what a mouse makes, and
// what the ear read "Yeah." in. Measured 2026-10-13 at a crest factor of 24
// to 26 dB with 20 to 120 ms of speech-level energy.
func click(seconds, spike float64) []byte {
	n := int(seconds * bridge.SampleRate)
	pcm := make([]byte, 2*n)
	r := rand.New(rand.NewPCG(1, 2))
	for i := range n {
		// Room floor, a few hundred counts, plus one short loud transient.
		v := float64(r.IntN(600) - 300)
		if t := float64(i) / bridge.SampleRate; t > 0.4 && t < 0.4+spike {
			v = 12000 * math.Sin(2*math.Pi*3000*t)
		}
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(v)))
	}
	return pcm
}

// speech is sustained voiced energy, as a sentence is.
func speech(seconds float64) []byte {
	n := int(seconds * bridge.SampleRate)
	pcm := make([]byte, 2*n)
	for i := range n {
		t := float64(i) / bridge.SampleRate
		v := 6000 * math.Sin(2*math.Pi*180*t) * (1 + 0.4*math.Sin(2*math.Pi*4*t))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(v)))
	}
	return pcm
}

// A click holds speech level for a few tens of milliseconds; a sentence holds
// it for most of its length. The floor sits between, so a transient the ear
// read a word in is not believed (ADR-0086).
//
// verifies SPEC §4.5
func TestSustainedSpeechSeparatesAClickFromASentence(t *testing.T) {
	const gate = 0.03 // the measured room's speech level (ADR-0083)
	for _, tc := range []struct {
		name   string
		pcm    []byte
		belief bool
	}{
		{"a 20 ms click", click(1.0, 0.02), false},
		{"a 120 ms click", click(1.2, 0.12), false},
		{"a short sentence", speech(0.8), true},
		{"an ordinary sentence", speech(2.5), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listen.Sustained(tc.pcm, gate)
			if believed := got >= listen.DefaultMinSustained; believed != tc.belief {
				t.Errorf("sustained %d ms, believed=%v, want believed=%v (floor %d ms)",
					1000*got/(2*bridge.SampleRate), believed, tc.belief,
					1000*listen.DefaultMinSustained/(2*bridge.SampleRate))
			}
		})
	}
}

// Silence sustains nothing, so it is never believed whatever the ear says.
//
// verifies SPEC §4.5
func TestSustainedSpeechOfSilenceIsNothing(t *testing.T) {
	if got := listen.Sustained(make([]byte, 2*bridge.SampleRate), 0.03); got != 0 {
		t.Errorf("silence sustained %d bytes, want 0", got)
	}
}
