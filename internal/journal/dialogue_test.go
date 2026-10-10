package journal_test

import (
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// reduceAll folds events numbered in order, as replay would.
func reduceAll(t *testing.T, events []journal.Event) journal.State {
	t.Helper()
	var s journal.State
	for i, e := range events {
		e.Seq = uint64(i + 1)
		var err error
		if s, err = journal.Reduce(s, e); err != nil {
			t.Fatalf("reduce %s: %v", e.Kind, err)
		}
	}
	return s
}

func ev(k journal.Kind, fields map[string]string) journal.Event {
	return journal.Event{Kind: k, Fields: fields}
}

// Teagan asks about the garage from the kitchen. The model says it is
// checking, reads the cover, and starts to answer; Teagan cuts it off. The
// next ask has to tell the model all of that, in the order it happened, with
// only the words Teagan actually heard.
//
// verifies SPEC §4.4
func TestTheDialogueIsWhatThePersonHeardInTheOrderItHappened(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "is the garage door closed", "speaker_id": "teagan"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"Let me check.","mode":"queue"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_get_state", "call_id": "call_c1", "args_json": `{"entity_id":"cover.garage_door"}`}),
		ev(journal.KindModelCompleted, map[string]string{"completion_json": "{}", "finish_reason": "stop"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "ok", "result_json": `{"entity_id":"cover.garage_door","state":"open"}`}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Let me check.", "frames_played": "19200", "call_id": "call_s1"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "ok"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s2", "args_json": `{"mode":"queue","streamed":true}`}),
		ev(journal.KindBargeInDetected, map[string]string{"tts_position_ms": "640"}),
		ev(journal.KindSpeechTruncated, map[string]string{
			"spoken_text": "The garage door is open,", "unspoken_text": " do you want me to close it?",
			"frames_played": "10240", "call_id": "call_s2",
		}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s2", "outcome": "cancelled"}),
	})

	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "is the garage door closed", Speaker: "teagan"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check."},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
		{Kind: journal.EntrySaid, CallID: "call_s2", Text: "The garage door is open,", Cut: true},
	}
	if !reflect.DeepEqual(s.Dialogue, want) {
		t.Errorf("dialogue =\n%+v\nwant\n%+v", s.Dialogue, want)
	}
}

// The model is asked again the moment its tools return, which is often
// while "Let me check." is still coming out of the speaker. It said those
// words, so it is told them; once playback is recorded they are the heard
// words instead.
//
// verifies SPEC §4.1
func TestSpeechStillPlayingIsInTheDialogueAsTheModelAskedForIt(t *testing.T) {
	events := []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "play something by led zeppelin"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"One sec, searching.","mode":"queue"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "media_search", "call_id": "call_c1", "args_json": `{"query":"led zeppelin"}`}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "ok", "result_json": `{"hits":3}`}),
	}
	s := reduceAll(t, events)
	if got := s.Dialogue[1]; got.Kind != journal.EntrySaid || got.Text != "One sec, searching." || !got.Pending {
		t.Errorf("still-playing speech = %+v, want pending with the words the model asked for", got)
	}

	s = reduceAll(t, append(events,
		ev(journal.KindSpeechSpoken, map[string]string{"text": "One sec, searching.", "frames_played": "28800", "call_id": "call_s1"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "ok"}),
	))
	if got := s.Dialogue[1]; got.Pending || got.Text != "One sec, searching." {
		t.Errorf("played speech = %+v, want settled", got)
	}
}

// A barge-in that empties the queue before an utterance plays leaves nothing
// heard. The model must not be told it said it (SPEC §4.4).
//
// verifies SPEC §4.4
func TestSpeechNobodyHeardIsNotInTheDialogue(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "what's the weather on saturday"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"Saturday looks sunny, with a high of seventy one.","mode":"queue"}`}),
		ev(journal.KindSpeechDiscarded, map[string]string{"unspoken_text": "Saturday looks sunny, with a high of seventy one.", "reason": "barge_in"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "cancelled"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "actually, what about sunday"}),
	})
	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "what's the weather on saturday"},
		{Kind: journal.EntryHeard, Text: "actually, what about sunday"},
	}
	if !reflect.DeepEqual(s.Dialogue, want) {
		t.Errorf("dialogue = %+v, want only what was heard: %+v", s.Dialogue, want)
	}
}

// Speculative work never happened as far as the household is concerned, so
// the model is not told about it either (SPEC §11).
//
// verifies SPEC §11
func TestSpeculativeCallsAreNotInTheDialogue(t *testing.T) {
	call := ev(journal.KindToolCalled, map[string]string{"tool": "ha_get_state", "call_id": "call_spec", "args_json": `{"entity_id":"sensor.living_room_temperature"}`})
	call.Speculative = true
	s := reduceAll(t, []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "how warm is the living room"}),
		call,
	})
	if len(s.Dialogue) != 1 {
		t.Errorf("dialogue = %+v, want only the question", s.Dialogue)
	}
}

// A failure is a result the model reasons about, so it is in the dialogue
// with its outcome rather than missing from it (SPEC §7).
//
// verifies SPEC §7
func TestAFailedCallIsInTheDialogueWithItsOutcome(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "turn off the kitchen lights"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_call_service", "call_id": "call_c1", "args_json": `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "timed_out", "result_json": `{"error":"timed_out"}`}),
	})
	got := s.Dialogue[len(s.Dialogue)-1]
	want := journal.Entry{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_call_service", Outcome: "timed_out", Result: `{"error":"timed_out"}`}
	if got != want {
		t.Errorf("last entry = %+v, want %+v", got, want)
	}
}

// Speech the session said for a slow call's acknowledgement is tied to that
// call, so the model can be shown the call it made rather than a speak it
// never did (ADR-0039).
//
// verifies SPEC §4.4
func TestAnAcknowledgementIsTiedToItsCall(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "recommend a movie like indiana jones starring tom holland", "speaker_id": "alan"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "media_search", "call_id": "call_c1", "args_json": `{"query":"Tom Holland adventure","acknowledgement":"Let me look through the library."}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_c1_ack", "args_json": `{"text":"Let me look through the library.","mode":"queue","acknowledges":"call_c1"}`}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Let me look through the library.", "frames_played": "30400", "call_id": "call_c1_ack"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1_ack", "outcome": "ok"}),
	})
	want := journal.Entry{Kind: journal.EntrySaid, CallID: "call_c1_ack", Text: "Let me look through the library.", Acknowledges: "call_c1"}
	if got := s.Dialogue[2]; got != want {
		t.Errorf("acknowledgement = %+v, want %+v", got, want)
	}
}
