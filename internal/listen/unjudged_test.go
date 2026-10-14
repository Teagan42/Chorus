package listen

import (
	"encoding/binary"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
)

// loud is n seconds of audio the trim keeps whole.
func loud(seconds float64) []byte {
	pcm := make([]byte, 2*int(seconds*bridge.SampleRate))
	for i := range len(pcm) / 2 {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(int16(8000)))
	}
	return pcm
}

// A household's own short command ranks them first without reaching the
// accept threshold, so the score is not evidence and the ranking is: the
// speaker keeps the turn rather than becoming a guest halfway through a
// conversation (ADR-0085).
//
// verifies SPEC §5
func TestAShortCommandLeavesTheSpeakerSpeaking(t *testing.T) {
	out := identity.Outcome{Reason: identity.BelowThreshold, Score: 0.62, Nearest: "teagan", RunnerUp: "bob", RunnerScore: 0.25, Leads: true}
	got := unjudged(out, loud(1.5), "teagan")
	if got.Reason != identity.TooShort {
		t.Errorf("reason = %q, want %q so attribution keeps the speaker", got.Reason, identity.TooShort)
	}
	if got.PersonID != "" {
		t.Errorf("person = %q; too_short names nobody, it declines to judge", got.PersonID)
	}
}

// Everything that must still be a verdict. Each of these is a voice the
// household did not want answered as themselves.
//
// verifies SPEC §5
func TestWhatStaysAVerdict(t *testing.T) {
	lead := identity.Outcome{Reason: identity.BelowThreshold, Score: 0.62, Nearest: "teagan", RunnerUp: "bob", RunnerScore: 0.25, Leads: true}
	for _, tc := range []struct {
		name    string
		out     identity.Outcome
		pcm     []byte
		speaker string
	}{
		{"a guest, nearest somebody else", withNearest(lead, "bob"), loud(1.5), "teagan"},
		{"a voice leading nobody, because nobody else is enrolled", withLeads(lead, false), loud(1.5), "teagan"},
		{"long enough to judge on its own", lead, loud(5), "teagan"},
		{"no session, so nobody to inherit from", lead, loud(1.5), ""},
		{"ambiguous, which judged the voice and could not choose", withReason(lead, identity.Ambiguous), loud(1.5), "teagan"},
		{"identified, which needs no help", withReason(lead, identity.Identified), loud(1.5), "teagan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unjudged(tc.out, tc.pcm, tc.speaker); got.Reason == identity.TooShort {
				t.Errorf("reason became %q; this voice must keep its verdict", got.Reason)
			}
		})
	}
}

func withNearest(o identity.Outcome, id string) identity.Outcome { o.Nearest = id; return o }
func withLeads(o identity.Outcome, b bool) identity.Outcome      { o.Leads = b; return o }
func withReason(o identity.Outcome, r identity.Reason) identity.Outcome {
	o.Reason = r
	return o
}
