package session_test

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Each utterance says which of the endpointer's rules ended it and at what
// probability, so a household tunes Smart Turn's bands from its own kitchen
// rather than from rendered voices (ADR-0094). A rule no verdict decided
// carries no probability, and an endpointer that cannot say leaves both out.
//
// verifies SPEC §4.5, §8
func TestAnUtteranceRecordsHowItsTurnEnded(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r := newRig(t, steps, nil)
	s := r.open(t, "teagan")
	for _, tr := range []session.Transcript{
		{Text: "turn off the kitchen lights", TurnEnd: "finished", TurnProbability: 0.988},
		{Text: "turn on the porch light", TurnEnd: "unsure", TurnProbability: 0.704},
		{Text: "set a timer for", TurnEnd: "dangling", TurnProbability: 0.9884},
		{Text: "is the garage door closed", TurnEnd: "silence", TurnProbability: 0.5},
		{Text: "good night"},
	} {
		tr.SpeakerID, tr.SpeakerMatch, tr.AudioRef = "teagan", "identified", "blob://mic/teagan"
		if err := s.Heard(context.Background(), tr); err != nil {
			t.Fatalf("heard %q: %v", tr.Text, err)
		}
	}

	events, err := r.store.Events(context.Background(), s.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	want := map[string][2]string{
		"turn off the kitchen lights": {"finished", "0.988"},
		"turn on the porch light":     {"unsure", "0.704"},
		"set a timer for":             {"dangling", "0.988"},
		"is the garage door closed":   {"silence", ""},
		"good night":                  {"", ""},
	}
	seen := 0
	for _, e := range events {
		if e.Kind != journal.KindUtteranceTranscribed {
			continue
		}
		w, ok := want[e.Fields["text"]]
		if !ok {
			continue
		}
		seen++
		end, endOK := e.Fields["turn_end"]
		p, pOK := e.Fields["turn_probability"]
		if end != w[0] || endOK != (w[0] != "") || p != w[1] || pOK != (w[1] != "") {
			t.Errorf("%q journalled turn_end=%q (%v) turn_probability=%q (%v), want %q and %q",
				e.Fields["text"], end, endOK, p, pOK, w[0], w[1])
		}
	}
	if seen != len(want) {
		t.Errorf("found %d of the %d utterances", seen, len(want))
	}
}
