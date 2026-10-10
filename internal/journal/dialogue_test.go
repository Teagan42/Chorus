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

// Kokoro falls over halfway through Alice's forecast, and the kitchen says
// it lost its voice. The next ask is told the voice cut the answer, not
// Alice, and the apology is marked as nobody's turn's words (ADR-0051).
//
// verifies SPEC §4.4, §7
func TestAVoiceFailureIsToldAsTheVoiceAndTheApologyAsCanned(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "alice"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "what's the weather tomorrow", "speaker_id": "alice"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"Tomorrow will be sunny, with a high of nineteen.","mode":"queue"}`}),
		ev(journal.KindModelCompleted, map[string]string{"completion_json": "{}", "finish_reason": "stop"}),
		ev(journal.KindSpeechFailed, map[string]string{"call_id": "call_s1", "reason": "tts_unavailable", "error": "kokoro: 503 Service Unavailable", "canned_call_id": "cn_3f9c2a10"}),
		ev(journal.KindSpeechTruncated, map[string]string{
			"spoken_text": "Tomorrow will be sunny, ", "unspoken_text": "with a high of nineteen.",
			"frames_played": "12000", "call_id": "call_s1", "reason": "tts_unavailable",
		}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "error", "result_json": `{"error":"tts_unavailable"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "cn_3f9c2a10", "args_json": `{"text":"Sorry, I've lost my voice for a moment.","mode":"queue","canned":true}`}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Sorry, I've lost my voice for a moment.", "frames_played": "30400", "call_id": "cn_3f9c2a10"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "cn_3f9c2a10", "outcome": "ok"}),
	})

	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "what's the weather tomorrow", Speaker: "alice"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Tomorrow will be sunny, ", Cut: true, CutBy: "tts_unavailable"},
		{Kind: journal.EntrySaid, CallID: "cn_3f9c2a10", Text: "Sorry, I've lost my voice for a moment.", Canned: true},
	}
	if !reflect.DeepEqual(s.Dialogue, want) {
		t.Errorf("dialogue =\n%+v\nwant\n%+v", s.Dialogue, want)
	}
}

// Ollama refused the connection, so the turn has no completion: the
// failure and the apology are all the log holds, and replay folds both.
//
// verifies SPEC §7, §8
func TestAModelThatNeverAnsweredReplaysToTheApology(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "turn off the kitchen lights", "speaker_id": "teagan"}),
		ev(journal.KindModelFailed, map[string]string{"reason": "unavailable", "error": "dial tcp 10.0.0.20:11434: connect: connection refused", "canned_call_id": "cn_77d01b4e"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "cn_77d01b4e", "args_json": `{"text":"Sorry, I can't think straight right now. Give me a minute and ask again.","mode":"queue","canned":true}`}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Sorry, I can't think straight right now. Give me a minute and ask again.", "frames_played": "64000", "call_id": "cn_77d01b4e"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "cn_77d01b4e", "outcome": "ok"}),
	})
	if len(s.Completions) != 0 || s.Interrupted {
		t.Errorf("state = %+v, want no completion and no interruption", s)
	}
	if n := len(s.Dialogue); n != 2 || !s.Dialogue[1].Canned {
		t.Errorf("dialogue = %+v, want the utterance and the canned apology", s.Dialogue)
	}
}

// interjected is Teagan's forecast paused for the garage door: the model
// interjects while the forecast plays, and the rest of it is held.
func interjected() []journal.Event {
	return []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "what's the weather tomorrow and is the garage shut", "speaker_id": "teagan"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"Tomorrow will be cloudy in the morning, with rain from three.","mode":"queue"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_get_state", "call_id": "call_c1", "args_json": `{"entity_id":"cover.garage_door"}`}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "ok", "result_json": `{"entity_id":"cover.garage_door","state":"open"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s2", "args_json": `{"text":"Sorry, the garage door is open.","mode":"interject"}`}),
		ev(journal.KindSpeechTruncated, map[string]string{
			"spoken_text": "Tomorrow will be cloudy in the morning,", "unspoken_text": " with rain from three.",
			"frames_played": "24000", "call_id": "call_s1", "reason": "interjected",
		}),
	}
}

// An interjection pauses what is playing, and the rest plays after it. The
// paused call is still playing while the interjection is said, and once it
// resumes the model is told everything Teagan heard of it, in one call.
//
// verifies SPEC §4.2
func TestAnInterjectedCallIsHeardWholeOnceItResumes(t *testing.T) {
	s := reduceAll(t, interjected())
	if got := s.Dialogue[1]; got.Text != "Tomorrow will be cloudy in the morning," || !got.Pending || got.Cut {
		t.Errorf("paused forecast = %+v, want still playing with the words heard so far", got)
	}

	s = reduceAll(t, append(interjected(),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Sorry, the garage door is open.", "frames_played": "36800", "call_id": "call_s2"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s2", "outcome": "ok"}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": " with rain from three.", "frames_played": "56000", "call_id": "call_s1"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "ok"}),
	))
	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "what's the weather tomorrow and is the garage shut", Speaker: "teagan"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Tomorrow will be cloudy in the morning, with rain from three."},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
		{Kind: journal.EntrySaid, CallID: "call_s2", Text: "Sorry, the garage door is open."},
	}
	if !reflect.DeepEqual(s.Dialogue, want) {
		t.Errorf("dialogue =\n%+v\nwant\n%+v", s.Dialogue, want)
	}
	if s.Interrupted || len(s.Unspoken) != 0 {
		t.Errorf("interrupted=%v unspoken=%q: a pause is not a cut", s.Interrupted, s.Unspoken)
	}
	if want := []string{"Tomorrow will be cloudy in the morning,", "Sorry, the garage door is open.", " with rain from three."}; !reflect.DeepEqual(s.Spoken, want) {
		t.Errorf("spoken = %q, want %q", s.Spoken, want)
	}
}

// Teagan cuts off the interjection, so the held rest of the forecast never
// plays. The model is told the forecast as far as Teagan heard it, cut by
// Teagan: not still playing, and not dropped.
//
// verifies SPEC §4.2, §4.4
func TestABargeInDuringAnInterjectionCutsTheCallItPaused(t *testing.T) {
	s := reduceAll(t, append(interjected(),
		ev(journal.KindBargeInDetected, map[string]string{"tts_position_ms": "480"}),
		ev(journal.KindSpeechTruncated, map[string]string{
			"spoken_text": "Sorry,", "unspoken_text": " the garage door is open.",
			"frames_played": "7680", "call_id": "call_s2", "reason": "barge_in",
		}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s2", "outcome": "cancelled"}),
		ev(journal.KindSpeechDiscarded, map[string]string{"unspoken_text": " with rain from three.", "call_id": "call_s1", "reason": "barge_in"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "cancelled"}),
	))
	if got := s.Dialogue[1]; got.Text != "Tomorrow will be cloudy in the morning," || !got.Cut || got.CutBy != "barge_in" || got.Pending {
		t.Errorf("forecast = %+v, want the heard half, cut by the barge-in", got)
	}
	if got := s.Dialogue[4]; got.Text != "Sorry," || !got.Cut {
		t.Errorf("interjection = %+v, want cut after its first word", got)
	}
}

// The kitchen had played all of the forecast generated so far when the
// interjection landed, so nothing was paused mid-word: the forecast was
// heard to there, and what the model went on to say plays after.
//
// verifies SPEC §4.2
func TestSpeechAfterAnInterjectionContinuesALineHeardToItsEnd(t *testing.T) {
	events := interjected()
	events[len(events)-1] = ev(journal.KindSpeechSpoken, map[string]string{"text": "Tomorrow will be cloudy in the morning,", "frames_played": "24000", "call_id": "call_s1"})
	s := reduceAll(t, append(events,
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Sorry, the garage door is open.", "frames_played": "36800", "call_id": "call_s2"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s2", "outcome": "ok"}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": " with rain from three.", "frames_played": "56000", "call_id": "call_s1"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "ok"}),
	))
	if got := s.Dialogue[1]; got.Text != "Tomorrow will be cloudy in the morning, with rain from three." || got.Cut || got.Pending {
		t.Errorf("forecast = %+v, want both halves, heard", got)
	}
}

// The voice fails before the held rest can resume, with nothing of it left
// to discard. What Teagan heard before the interjection stays in the
// dialogue: the call's result ends the playing, not the hearing.
//
// verifies SPEC §4.4, §7
func TestAPausedCallThatNeverResumesKeepsWhatWasHeard(t *testing.T) {
	s := reduceAll(t, append(interjected(),
		ev(journal.KindSpeechFailed, map[string]string{"call_id": "call_s1", "reason": "tts_unavailable", "error": "open: kokoro: 503 Service Unavailable"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "error", "result_json": `{"error":"tts_unavailable"}`}),
	))
	if got := s.Dialogue[1]; got.Text != "Tomorrow will be cloudy in the morning," || got.Pending || got.Held {
		t.Errorf("forecast = %+v, want settled with the words heard", got)
	}
}
