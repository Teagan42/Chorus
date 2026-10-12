package journal_test

import (
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// A streamed speak call is recorded at its first words, with none in its
// arguments; the completion that streamed it records them. An ask made
// while "Let me check the garage door." is still playing is told the words,
// pending, as it is told a speak that arrived whole, and replay tells it
// the same from the same log (ADR-0077). Playback then says what was heard.
//
// verifies SPEC §4.1, §4.4
func TestStreamedSpeechStillPlayingIsToldTheWordsItsCompletionRecorded(t *testing.T) {
	events := []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "is the garage door closed", "speaker_id": "teagan"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"mode":"queue","streamed":true}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_get_state", "call_id": "call_c1", "args_json": `{"entity_id":"cover.garage_door"}`}),
		ev(journal.KindModelCompleted, map[string]string{
			"completion_json": "{}", "finish_reason": "tool_calls",
			"streamed_speech_json": `{"call_s1":"Let me check the garage door."}`,
		}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "ok", "result_json": `{"state":"open"}`}),
	}
	s := reduceAll(t, events)
	if got, want := s.Dialogue[1], (journal.Entry{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check the garage door.", Pending: true}); got != want {
		t.Errorf("streamed speech still playing = %+v, want %+v", got, want)
	}

	s = reduceAll(t, append(events,
		ev(journal.KindSpeechTruncated, map[string]string{"spoken_text": "Let me check", "unspoken_text": " the garage door.", "frames_played": "9600", "call_id": "call_s1", "reason": "barge_in"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "cancelled"}),
	))
	if got := s.Dialogue[1]; got.Text != "Let me check" || !got.Cut || got.Pending {
		t.Errorf("cut streamed speech = %+v, want only the heard words", got)
	}
}

// Words a completion records never replace what playback already said was
// heard, and a completion that streamed nothing, as every one on Ollama
// does, changes nothing.
//
// verifies SPEC §4.4
func TestAStreamedCompletionNeverOverridesWhatWasHeard(t *testing.T) {
	s := reduceAll(t, []journal.Event{
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "what's the weather tomorrow"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"mode":"queue","streamed":true}`}),
		ev(journal.KindSpeechTruncated, map[string]string{"spoken_text": "Tomorrow", "unspoken_text": " will be sunny.", "frames_played": "4800", "call_id": "call_s1", "reason": "barge_in"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "cancelled"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s2", "args_json": `{"mode":"queue","streamed":true}`}),
		ev(journal.KindModelCompleted, map[string]string{
			"completion_json": "{}", "finish_reason": "error",
			"streamed_speech_json": `{"call_s1":"Tomorrow will be sunny.","call_s2":""}`,
		}),
		ev(journal.KindModelCompleted, map[string]string{"completion_json": "{}", "finish_reason": "stop"}),
	})
	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "what's the weather tomorrow"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Tomorrow", Cut: true, CutBy: "barge_in"},
		{Kind: journal.EntrySaid, CallID: "call_s2", Pending: true},
	}
	if !reflect.DeepEqual(s.Dialogue, want) {
		t.Errorf("dialogue =\n%+v\nwant\n%+v", s.Dialogue, want)
	}

	if _, err := journal.Reduce(journal.State{}, ev(journal.KindModelCompleted, map[string]string{"completion_json": "{}", "finish_reason": "stop", "streamed_speech_json": `["Let me check."]`})); err == nil {
		t.Error("an unreadable streamed_speech_json was folded without complaint")
	}
}
