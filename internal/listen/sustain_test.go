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
	at := int(0.4 * bridge.SampleRate)
	// A sharp onset decaying over the spike's length: measured clicks sat at
	// a crest factor of 24 to 26 dB, which a sine burst does not reach.
	decay := spike * bridge.SampleRate
	for i := range n {
		v := float64(r.IntN(200) - 100)
		if i >= at && float64(i-at) < decay {
			k := float64(i - at)
			v = 14000 * math.Exp(-6*k/decay) * math.Sin(2*math.Pi*2600*k/bridge.SampleRate)
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

// A click is a spike in a quiet room; a voice is not, however briefly it
// speaks. Shape decides, because length does not: a quiet one-word answer
// holds as little speech level as a click and must still be believed
// (ADR-0090).
//
// verifies SPEC §4.5
func TestAnImpulseIsToldFromAVoiceByItsShape(t *testing.T) {
	const gate = 0.03 // the measured room's speech level (ADR-0083)
	for _, tc := range []struct {
		name    string
		pcm     []byte
		impulse bool
	}{
		{"a 20 ms click", click(1.0, 0.02), true},
		{"a 120 ms click", click(1.2, 0.12), true},
		{"one quiet word", softWord(0.7, 0.12), false},
		{"a short sentence", speech(0.8), false},
		{"an ordinary sentence", speech(2.5), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := listen.Impulsive(tc.pcm, gate, listen.DefaultMinSustained, 0); got != tc.impulse {
				t.Errorf("Impulsive = %v, want %v", got, tc.impulse)
			}
		})
	}
}

// The ceiling is the satellite's: a room whose voices crest higher than
// the measured one raises it, and the same click is then believed, while
// zero keeps the measured 22 dB (ADR-0098).
//
// verifies SPEC §4.5
func TestTheClickCeilingIsTheSatellites(t *testing.T) {
	const gate = 0.03
	c := click(1.0, 0.02)
	if !listen.Impulsive(c, gate, listen.DefaultMinSustained, 0) {
		t.Fatal("zero did not take the measured default")
	}
	if !listen.Impulsive(c, gate, listen.DefaultMinSustained, listen.DefaultMaxCrest) {
		t.Error("the default, named, judged differently from zero")
	}
	if listen.Impulsive(c, gate, listen.DefaultMinSustained, 30) {
		t.Error("a 30 dB ceiling still called a ~25 dB click an impulse")
	}
	// Lowering it never makes a sustained sentence a click: length still
	// has to fail as well as shape.
	if listen.Impulsive(speech(2.5), gate, listen.DefaultMinSustained, 4) {
		t.Error("a sentence was judged by shape alone")
	}
}

// softWord is one short word spoken softly: little sustained energy, but a
// voice's crest factor rather than a spike's.
func softWord(seconds, word float64) []byte {
	n := int(seconds * bridge.SampleRate)
	pcm := make([]byte, 2*n)
	from := int(0.2 * bridge.SampleRate)
	for i := range n {
		t := float64(i) / bridge.SampleRate
		v := 150 * math.Sin(2*math.Pi*200*t)
		if i >= from && i < from+int(word*bridge.SampleRate) {
			v = 1100 * math.Sin(2*math.Pi*190*t) * (1 + 0.5*math.Sin(2*math.Pi*9*t))
		}
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(v)))
	}
	return pcm
}

// Silence sustains nothing, so it is never believed whatever the ear says.
//
// verifies SPEC §4.5
func TestSustainedSpeechOfSilenceIsNothing(t *testing.T) {
	if got := listen.Sustained(make([]byte, 2*bridge.SampleRate), 0.03); got != 0 {
		t.Errorf("silence sustained %d bytes, want 0", got)
	}
}
