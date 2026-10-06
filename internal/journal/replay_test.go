package journal_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// conversation writes one interrupted turn with a tool call, and returns the
// store so replay reads exactly what the live reduction saw.
func conversation(t *testing.T) *journal.MemStore {
	t.Helper()

	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), versions())
	records := []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob/1", Fields: map[string]string{"text": "play something"}},
		{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": `{"say":"one sec"}`, "finish_reason": "tool_calls"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "media_search", "call_id": "c1", "args_json": "{}"}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob/2", Fields: map[string]string{"text": "one sec", "frames_played": "16000"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c1", "outcome": "ok", "result_json": `{"hits":3}`}},
		{Kind: journal.KindBargeInDetected, AudioRef: "blob/3", Fields: map[string]string{"tts_position_ms": "420"}},
		{Kind: journal.KindSpeechTruncated, AudioRef: "blob/4", Fields: map[string]string{
			"spoken_text": "I found three", "unspoken_text": " albums by that artist", "frames_played": "6720",
		}},
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended"}},
	}
	for _, r := range records {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// verifies SPEC §8
func TestReplayDerivesStateFromTheLog(t *testing.T) {
	got, err := journal.Replay(context.Background(), conversation(t), "conv-1", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}

	want := journal.State{
		ConversationID: "conv-1",
		Satellite:      "kitchen",
		Speaker:        "teagan",
		Open:           false,
		CloseReason:    "model_ended",
		LastSeq:        9,
		Heard:          []string{"play something"},
		Spoken:         []string{"one sec", "I found three"},
		Unspoken:       []string{" albums by that artist"},
		Interrupted:    true,
		BargeInAt:      []time.Duration{420 * time.Millisecond},
		Completions:    []string{`{"say":"one sec"}`},
		Calls: []journal.Call{{
			ID: "c1", Tool: "media_search", Args: "{}",
			Outcome: "ok", Result: `{"hits":3}`,
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("state mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

// verifies SPEC §8
func TestReplayIsDeterministic(t *testing.T) {
	store := conversation(t)

	first, err := journal.Replay(context.Background(), store, "conv-1", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	second, err := journal.Replay(context.Background(), store, "conv-1", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("replay is not deterministic\n%+v\n%+v", first, second)
	}
}

// verifies SPEC §8
func TestReplaySubstitutesOverriddenNondeterministicInputs(t *testing.T) {
	got, err := journal.Replay(context.Background(), conversation(t), "conv-1", journal.Overrides{
		Completions: map[uint64]string{3: `{"say":"checking"}`},
		ToolResults: map[string]string{"c1": `{"hits":0}`},
	})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if want := []string{`{"say":"checking"}`}; !reflect.DeepEqual(got.Completions, want) {
		t.Errorf("completions = %v, want %v", got.Completions, want)
	}
	if got.Calls[0].Result != `{"hits":0}` {
		t.Errorf("tool result = %q, want the override", got.Calls[0].Result)
	}
}

// verifies SPEC §11
func TestReplayKeepsSpeculativeWorkOutOfCommittedState(t *testing.T) {
	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(0, 0)), versions())
	ctx := context.Background()

	if _, err := j.Append(ctx, "conv-1", journal.Record{
		Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Prefill on an STT partial that the user then talked over (SPEC §11).
	if _, err := j.Append(ctx, "conv-1", journal.Record{
		Kind:        journal.KindModelCompleted,
		Speculative: true,
		Fields:      map[string]string{"completion_json": `{"say":"discarded"}`, "finish_reason": "stop"},
	}); err != nil {
		t.Fatalf("append speculative: %v", err)
	}

	state, err := journal.Replay(ctx, store, "conv-1", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(state.Completions) != 0 {
		t.Errorf("speculative completion leaked into state: %v", state.Completions)
	}
	if state.Speculative != 1 {
		t.Errorf("speculative count = %d, want 1", state.Speculative)
	}
}

// verifies SPEC §8
func TestReduceHandlesEveryGeneratedKind(t *testing.T) {
	for _, k := range journal.AllKinds {
		if !journal.Handled(k) {
			t.Errorf("reducer does not handle %q; replay would be lossy", k)
		}
	}
}
