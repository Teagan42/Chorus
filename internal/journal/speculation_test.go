package journal_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// teaganInTheKitchen is a conversation one turn in: the kitchen lights are
// on and the model said so.
func teaganInTheKitchen(t *testing.T, j *journal.Journal) {
	t.Helper()
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan", "room": "kitchen"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/1", Fields: map[string]string{"text": "are the kitchen lights on", "speaker_id": "teagan", "speaker_match": "identified"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"text":"They are."}`}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/s1", Fields: map[string]string{"text": "They are.", "frames_played": "12800", "call_id": "s1"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s1", "outcome": "ok"}},
	} {
		if _, err := j.Append(context.Background(), "conv-kitchen", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
}

// What a pause supposes is what the log says once the utterance is
// appended: the same reducer folds both, so an ask made from the supposition
// is the ask the confirmed turn would make.
//
// verifies SPEC §8, §11
func TestSupposingAnUtteranceIsAppendingIt(t *testing.T) {
	at := time.Date(2026, 10, 9, 18, 4, 12, 0, time.UTC)
	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(at), versions())
	teaganInTheKitchen(t, j)
	ctx := context.Background()

	before, err := journal.Replay(ctx, store, "conv-kitchen", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	next := journal.Record{
		Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/2",
		Fields: map[string]string{"text": "turn them off", "speaker_id": "teagan", "speaker_match": "identified"},
	}
	supposed, err := journal.Suppose(before, at, next)
	if err != nil {
		t.Fatalf("suppose: %v", err)
	}
	if _, err := j.Append(ctx, "conv-kitchen", next); err != nil {
		t.Fatalf("append: %v", err)
	}
	after, err := journal.Replay(ctx, store, "conv-kitchen", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !reflect.DeepEqual(supposed, after) {
		t.Errorf("supposed state differs from the appended one\nsupposed %+v\nappended %+v", supposed, after)
	}
	// The supposition wrote nothing.
	if events, _ := store.Events(ctx, "conv-kitchen"); len(events) != 6 {
		t.Errorf("log holds %d events, want the 5 written and the 1 appended", len(events))
	}
}

// verifies SPEC §8
func TestSupposingAnUndeclaredKindFails(t *testing.T) {
	if _, err := journal.Suppose(journal.State{}, time.Unix(0, 0), journal.Record{Kind: "utterance_guessed"}); err == nil {
		t.Fatal("supposed an undeclared kind")
	}
}

// A guess the person talked over is in the log, and replay is the same as if
// it had never been made: the household never heard it, and nothing it
// called ran (SPEC §11).
//
// verifies SPEC §8, §11, §15
func TestADiscardedSpeculationLeavesReplayAsItWas(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 9, 18, 4, 12, 0, time.UTC)

	plain := journal.NewMemStore()
	teaganInTheKitchen(t, journal.New(plain, journal.FixedClock(at), versions()))
	guessed := journal.NewMemStore()
	j := journal.New(guessed, journal.FixedClock(at), versions())
	teaganInTheKitchen(t, j)
	if _, err := j.Append(ctx, "conv-kitchen", journal.Record{
		Kind: journal.KindSpeculationDiscarded, Speculative: true,
		Fields: map[string]string{
			"text": "turn them off", "reason": "resumed", "speaker_id": "teagan",
			"completion_json": `{"tool_calls":[{"function":{"name":"ha_call_service","arguments":{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}}}]}`,
			"finish_reason":   "tool_calls", "ran_ms": "340",
		},
	}); err != nil {
		t.Fatalf("append speculation: %v", err)
	}

	want, err := journal.Replay(ctx, plain, "conv-kitchen", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := journal.Replay(ctx, guessed, "conv-kitchen", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if got.Speculative != 1 {
		t.Errorf("speculative = %d, want the one discard counted", got.Speculative)
	}
	got.Speculative, got.LastSeq = want.Speculative, want.LastSeq
	if !reflect.DeepEqual(got, want) {
		t.Errorf("a discarded guess changed replay\ngot  %+v\nwant %+v", got, want)
	}
}
