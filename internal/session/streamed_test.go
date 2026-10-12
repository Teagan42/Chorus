package session_test

import (
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// asked waits until the engine has been asked n times.
func (r *rig) asked(t *testing.T, n int) []session.Input {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if asks := r.engine.asks(); len(asks) >= n {
			return asks
		}
		runtime.Gosched()
	}
	t.Fatalf("the model was never asked %d times", n)
	return nil
}

// Teagan asks about the garage door. The model's "Let me check the garage
// door." streams in two deltas, as vLLM's speak calls do, while it reads the
// cover, and the follow-up is asked while those words are still playing. It
// is told them, pending, as it is told a speak that arrived whole, so it
// does not say them again; and a replay of the log tells it the same
// dialogue, because the completion recorded the words (ADR-0077).
//
// verifies SPEC §4.1, §4.4, §8
func TestAFollowUpIsToldStreamedWordsStillPlaying(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Let me check ", Mode: session.ModeQueue}},
		{act: session.SpeechDelta{CallID: "call_s1", Text: "the garage door.", Mode: session.ModeQueue, Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{"ha_get_state": reads(garageState)})
	r.engine.then = [][]step{{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}}
	r.speaker.hold = true

	s := r.open(t, "teagan")
	errc := heard(s, "is the garage door closed")
	followUp := r.asked(t, 2)[1]

	said := journal.Entry{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check the garage door.", Pending: true}
	if got := followUp.Dialogue[1]; got != said {
		t.Errorf("the follow-up was told %+v, want the streamed words still playing %+v", got, said)
	}
	r.awaitKind(t, s.ConversationID(), journal.KindModelCompleted)
	if replayed := r.state(t, s.ConversationID()).Dialogue; !reflect.DeepEqual(replayed, followUp.Dialogue) {
		t.Errorf("replayed dialogue =\n%+v\nthe follow-up was asked with\n%+v", replayed, followUp.Dialogue)
	}
	events, err := r.store.Events(t.Context(), s.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == journal.KindModelCompleted {
			if got := e.Fields["streamed_speech_json"]; got != `{"call_s1":"Let me check the garage door."}` {
				t.Errorf("the first ask's streamed_speech_json = %q", got)
			}
			break
		}
	}

	close(r.speaker.release)
	wait(t, errc)
}

// A speak call that arrives whole, as every Ollama one does, carries its
// words in its tool_called; its completion records nothing more, so the
// Ollama path journals exactly what it did.
//
// verifies SPEC §8
func TestAWholeSpeakCallAddsNothingToItsCompletion(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Turning off the kitchen lights.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	s := r.open(t, "alan")
	wait(t, heard(s, "turn off the kitchen lights"))

	completed := r.eventOf(t, s.ConversationID(), journal.KindModelCompleted)
	if _, ok := completed.Fields["streamed_speech_json"]; ok {
		t.Errorf("model_completed = %v, want no streamed words for a whole speak call", completed.Fields)
	}
}
