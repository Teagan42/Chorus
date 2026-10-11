package session_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// hear runs a turn over a whole transcript in the background, as the
// listener hands one over once the utterance has ended.
func hear(s *session.Session, t session.Transcript) <-chan error {
	if t.AudioRef == "" {
		t.AudioRef = "blob://mic/u2"
	}
	out := make(chan error, 1)
	go func() { out <- s.Heard(context.Background(), t) }()
	return out
}

// stopped is Teagan's forecast, cut by her one-word stop.
func stopped(t *testing.T, partial string) (*rig, *session.Session) {
	t.Helper()
	r, s, errc := forecasting(t, kitchenHousehold)
	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/u2", SpeakerID: "teagan", Energy: 0.9, Partial: partial,
	})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)
	return r, s
}

// Teagan's stop cut the forecast. Once she stops speaking, "stop" is the
// whole of what she said: it is recorded as a stop, and nobody asks the
// model what to say to it. The cut is what she asked for.
//
// verifies SPEC §4.3
func TestAStopThatCutSpeechIsNotAnswered(t *testing.T) {
	r, s := stopped(t, "stop")

	wait(t, hear(s, session.Transcript{Text: "Stop.", SpeakerID: "teagan", AudioRef: "blob://mic/u2", BargedIn: true}))

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want only the forecast", n)
	}
	heard := r.ofKind(t, s.ConversationID(), journal.KindUtteranceTranscribed)
	if len(heard) != 2 || heard[1].Fields["text"] != "Stop." || heard[1].Fields["hot_word"] != "stop" {
		t.Errorf("utterance_transcribed = %v, want the stop recorded as one", heard)
	}
	// The next turn is told she said it, after the half she heard.
	d := r.state(t, s.ConversationID()).Dialogue
	if last := d[len(d)-1]; last.Kind != journal.EntryHeard || last.Text != "Stop." {
		t.Errorf("dialogue ends %+v, want her stop", last)
	}
}

// "Never mind" over the forecast ends it the same way; and a partial that
// was "stop" but whose utterance went on to ask for something is a request
// the model answers.
//
// verifies SPEC §4.3
func TestOnlyAWholeHotUtteranceGoesUnanswered(t *testing.T) {
	r, s := stopped(t, "never mind")
	wait(t, hear(s, session.Transcript{Text: "Never mind.", SpeakerID: "teagan", BargedIn: true}))
	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("never mind: the model was asked %d times, want once", n)
	}
	if got := r.ofKind(t, s.ConversationID(), journal.KindUtteranceTranscribed)[1].Fields["hot_word"]; got != "never_mind" {
		t.Errorf("hot_word = %q, want never_mind", got)
	}

	r, s = stopped(t, "stop")
	close(r.speaker.release)
	wait(t, hear(s, session.Transcript{Text: "Stop the music in the kitchen.", SpeakerID: "teagan", BargedIn: true}))
	if n := len(r.engine.asks()); n != 2 {
		t.Errorf("a request: the model was asked %d times, want twice", n)
	}
	if got := r.ofKind(t, s.ConversationID(), journal.KindUtteranceTranscribed)[1].Fields["hot_word"]; got != "" {
		t.Errorf("hot_word = %q on a request", got)
	}
}

// "Stop" said with nothing playing stopped nothing, so it is an ordinary
// turn: the model is asked, as it was before hot phrases.
//
// verifies SPEC §4.3
func TestAStopThatStoppedNothingIsAnOrdinaryTurn(t *testing.T) {
	r := newRigWith(t, []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}, nil, nil, kitchenHousehold)
	s := r.open(t, "teagan")

	wait(t, hear(s, session.Transcript{Text: "Stop.", SpeakerID: "teagan"}))

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
	if got := r.eventOf(t, s.ConversationID(), journal.KindUtteranceTranscribed).Fields["hot_word"]; got != "" {
		t.Errorf("hot_word = %q, want none: the model answered it", got)
	}
}

// Teagan asked for the oven timer and a slow search, and is waiting in
// silence. "Never mind" reaches the working turn: the search, which a
// barge-in cancels, is cancelled; the timer, which outlives one, is kept.
//
// verifies SPEC §4.3, §4.4
func TestNeverMindEndsAWorkingTurnUnderEachCallsPolicy(t *testing.T) {
	steps := []step{
		// ha_get_state cancels on interrupt; timer_start detaches.
		{act: session.ToolCall{ID: "c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.ToolCall{ID: "c2", Tool: "timer_start", Args: `{"seconds":720,"label":"oven"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	look, oven := newGateTool(`{"state":"closed"}`), newGateTool(`{"timer_id":"t_3f9c2a10"}`)
	r := newRigWith(t, steps, map[string]session.Tool{"ha_get_state": look, "timer_start": oven}, nil, kitchenHousehold)
	s := r.open(t, "teagan")
	errc := heard(s, "is the garage shut, and set the oven for twelve minutes")
	look.enter(t)
	oven.enter(t)

	ok, err := s.BargeIn(t.Context(), session.Candidate{AudioRef: "blob://mic/u2", SpeakerID: "teagan", Energy: 0.9, Partial: "Never mind."})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)
	wait(t, hear(s, session.Transcript{Text: "Never mind.", SpeakerID: "teagan", BargedIn: true}))

	if got := r.awaitCall(t, s.ConversationID(), "c1").Outcome; got != "cancelled" {
		t.Errorf("garage look = %q, want cancelled", got)
	}
	close(oven.release)
	if got := r.awaitOutcome(t, s.ConversationID(), "c2", "detached"); got.Result != `{"timer_id":"t_3f9c2a10"}` {
		t.Errorf("oven timer = %+v, want it kept", got)
	}
	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
}

// Alice asks the weather, hears it through, and says "say that again". The
// kitchen says it again in the same words, as a speak call the orchestrator
// made, without asking the model.
//
// verifies SPEC §4.2, §4.3
func TestSayThatAgainRepeatsWhatWasSaid(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "One sec.", Last: true}},
		{act: session.SpeechDelta{CallID: "s2", Text: weather, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, kitchenHousehold)
	s := r.open(t, "alice")
	wait(t, heard(s, "what's the weather today"))

	wait(t, hear(s, session.Transcript{Text: "Say that again?", SpeakerID: "alice"}))

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want only the forecast", n)
	}
	spoken := r.ofKind(t, s.ConversationID(), journal.KindSpeechSpoken)
	if len(spoken) != 3 || spoken[2].Fields["text"] != "One sec. "+weather {
		t.Fatalf("speech_spoken = %v, want the turn said again", spoken)
	}
	again := spoken[2].Fields["call_id"]
	for _, e := range r.ofKind(t, s.ConversationID(), journal.KindToolCalled) {
		if e.Fields["call_id"] != again {
			continue
		}
		var args struct {
			Repeats bool `json:"repeats"`
		}
		if err := json.Unmarshal([]byte(e.Fields["args_json"]), &args); err != nil || e.Fields["tool"] != "speak" || !args.Repeats {
			t.Errorf("tool_called = %v, want a speak call marked as a repeat", e.Fields)
		}
	}
	heard := r.ofKind(t, s.ConversationID(), journal.KindUtteranceTranscribed)
	if heard[1].Fields["hot_word"] != "repeat" {
		t.Errorf("utterance_transcribed = %v, want it answered as a repeat", heard[1].Fields)
	}
}

// Alice talks over the forecast to ask for it again. What she heard is the
// part before the cut, and that is what is said again: the rest was never
// said, and the kitchen does not pretend it was.
//
// verifies SPEC §4.2, §4.3, §4.4
func TestARepeatSaysOnlyWhatWasHeard(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)
	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/u2", SpeakerID: "alice", Energy: 0.9, Partial: "say that again",
	})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)
	close(r.speaker.release)

	wait(t, hear(s, session.Transcript{Text: "Say that again.", SpeakerID: "alice", BargedIn: true}))

	if spoken := r.eventOf(t, s.ConversationID(), journal.KindSpeechSpoken); spoken.Fields["text"] != "Sunny this morning, " {
		t.Errorf("said again %q, want only the heard half", spoken.Fields["text"])
	}
	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
}

// Nothing has been said yet in a fresh conversation, so there is nothing
// to say again: "what did you say" goes to the model as an ordinary turn.
//
// verifies SPEC §4.3
func TestARepeatWithNothingSaidIsAnOrdinaryTurn(t *testing.T) {
	r := newRigWith(t, []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}, nil, nil, kitchenHousehold)
	s := r.open(t, "alice")

	wait(t, hear(s, session.Transcript{Text: "What did you say?", SpeakerID: "alice"}))

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
	if got := r.eventOf(t, s.ConversationID(), journal.KindUtteranceTranscribed).Fields["hot_word"]; got != "" {
		t.Errorf("hot_word = %q, want none", got)
	}
}
