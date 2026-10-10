package session_test

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// voiced runs one turn for a voice: who it matched, and how.
func voiced(s *session.Session, text, speaker, match string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{
			Text: text, SpeakerID: speaker, SpeakerMatch: match, AudioRef: "blob://mic/" + speaker,
		})
	}()
	return out
}

// Teagan asks the kitchen how she takes her coffee, and a friend over for
// brunch chimes in. The friend's voice matched nobody, so the turn is a
// guest's: it is not told Teagan's oat milk, nothing is recalled for it, and
// the log says the guest was told nothing, so a replay asks the same.
//
// verifies SPEC §5
func TestAGuestChimingInIsNotToldTheSpeakerBeforesMemories(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, mem := newMemoryRig(t, steps, nil, nil)
	s := r.open(t, "teagan")
	wait(t, voiced(s, "how do i take my coffee", "teagan", "identified"))
	wait(t, voiced(s, "ooh what's the wifi password", "", "below_threshold"))

	asks := r.engine.asks()
	if len(asks) != 2 {
		t.Fatalf("the model was asked %d times, want 2", len(asks))
	}
	if asks[0].Speaker != "teagan" || len(asks[0].Memories) == 0 {
		t.Fatalf("Teagan's turn was asked as %q with %+v", asks[0].Speaker, asks[0].Memories)
	}
	if guest := asks[1]; guest.Speaker != "" || len(guest.Memories) != 0 || len(guest.Summaries) != 0 {
		t.Errorf("the guest's turn was asked as %q with %+v and %+v, want a guest told nothing",
			guest.Speaker, guest.Memories, guest.Summaries)
	}
	if got := mem.asked(); len(got) != 1 || got[0].Person != "teagan" {
		t.Errorf("recalled for %+v, want Teagan's turn alone", got)
	}

	st := r.state(t, s.ConversationID())
	if st.Speaker != "" || len(st.Recalled) != 0 || st.RecalledFor != "" {
		t.Errorf("replayed as %q told %+v for %q, want the guest told nothing", st.Speaker, st.Recalled, st.RecalledFor)
	}
	events, err := r.store.Events(context.Background(), s.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	heardBy := map[string]string{}
	for _, e := range events {
		if e.Kind == journal.KindUtteranceTranscribed {
			heardBy[e.Fields["text"]] = e.Fields["speaker_match"]
		}
	}
	if heardBy["how do i take my coffee"] != "identified" || heardBy["ooh what's the wifi password"] != "below_threshold" {
		t.Errorf("speaker_match = %v, want how each voice matched", heardBy)
	}
}

// The guest asks to forget the oat milk by the id the previous turn was
// told. It is refused before anything runs: a guest forgets nothing, and
// certainly not as Teagan.
//
// verifies SPEC §5
func TestAGuestChimingInCannotForgetAsTheSpeakerBefore(t *testing.T) {
	forgets := make(chan string, 2)
	tool := session.ToolFunc(func(_ context.Context, args string) (string, error) {
		forgets <- args
		return `{"forgot":"m_3f9c2a10"}`, nil
	})
	r, _ := newMemoryRig(t, nil, map[string]session.Tool{"forget": tool}, nil)
	s := r.open(t, "teagan")
	wait(t, voiced(s, "how do i take my coffee", "teagan", "identified"))

	r.engine.mu.Lock()
	r.engine.steps = []step{
		{act: session.ToolCall{ID: "call_f1", Tool: "forget", Args: `{"memory_id":"m_3f9c2a10"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r.engine.mu.Unlock()
	wait(t, voiced(s, "forget the oat milk thing", "", "ambiguous"))

	c := callByID(t, r.state(t, s.ConversationID()), "call_f1")
	if c.Outcome != "error" || c.Result != `{"error":"unidentified_speaker"}` {
		t.Errorf("forget = %+v, want refused as unidentified", c)
	}
	if len(forgets) != 0 {
		t.Errorf("the guest's forget reached the executor: %s", <-forgets)
	}
}

// When nothing judged the voice, it is still Teagan's turn: an embedder that
// failed once is no evidence of anyone else.
//
// verifies SPEC §5
func TestAnUnjudgedUtteranceStaysWithTheSpeaker(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, _ := newMemoryRig(t, steps, nil, nil)
	s := r.open(t, "teagan")
	wait(t, voiced(s, "how do i take my coffee", "teagan", "identified"))
	wait(t, voiced(s, "and is it bin night", "", ""))

	asks := r.engine.asks()
	if last := asks[len(asks)-1]; last.Speaker != "teagan" || len(last.Memories) == 0 {
		t.Errorf("the unjudged turn was asked as %q with %+v, want Teagan's", last.Speaker, last.Memories)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 1 {
		t.Errorf("memory_recalled recorded %d times, want Teagan's once", n)
	}
}

// Teagan answers the guest, and her own memories come back with her.
//
// verifies SPEC §5
func TestTheSpeakerAfterAGuestIsToldTheirMemoriesAgain(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, _ := newMemoryRig(t, steps, nil, nil)
	s := r.open(t, "teagan")
	wait(t, voiced(s, "how do i take my coffee", "teagan", "identified"))
	wait(t, voiced(s, "ooh what's the wifi password", "", "below_threshold"))
	wait(t, voiced(s, "it's on the fridge isn't it", "teagan", "identified"))

	asks := r.engine.asks()
	if last := asks[len(asks)-1]; last.Speaker != "teagan" || len(last.Memories) != 2 {
		t.Errorf("Teagan's turn after the guest was asked as %q with %+v", last.Speaker, last.Memories)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 3 {
		t.Errorf("memory_recalled recorded %d times, want Teagan, the guest, and Teagan again", n)
	}
}
