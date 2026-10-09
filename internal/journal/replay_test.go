package journal_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// conversationRecords is one interrupted turn with a tool call. Shared with
// the store conformance suite so both backends replay the same conversation.
func conversationRecords() []journal.Record {
	return []journal.Record{
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
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended", "satellite": "kitchen"}},
	}
}

// wantReplayState is the state conversationRecords must reduce to, whichever
// Store holds the log.
func wantReplayState() journal.State {
	return journal.State{
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
		// The speech events predate naming their call, so each one lands
		// where it was heard.
		Dialogue: []journal.Entry{
			{Kind: journal.EntryHeard, Text: "play something"},
			{Kind: journal.EntryCall, CallID: "c1", Tool: "media_search", Args: "{}"},
			{Kind: journal.EntrySaid, Text: "one sec"},
			{Kind: journal.EntryResult, CallID: "c1", Tool: "media_search", Outcome: "ok", Result: `{"hits":3}`},
			{Kind: journal.EntrySaid, Text: "I found three", Cut: true},
		},
	}
}

// conversation writes conversationRecords and returns the store, so replay
// reads exactly what the live reduction saw.
func conversation(t *testing.T) *journal.MemStore {
	t.Helper()

	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), versions())
	for _, r := range conversationRecords() {
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

	want := wantReplayState()
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

// A migration closes one session and opens another in the same log, so the
// reducer sees Open go false and back to true. What it must end on is the
// satellite the person is actually at, with no close reason left over: the
// conversation never ended, only its device changed (SPEC §4.5).
//
// verifies SPEC §4.5, §8
func TestReplayFollowsAConversationToItsNewSatellite(t *testing.T) {
	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(time.Unix(0, 0)), versions())
	ctx := context.Background()

	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{
			"satellite": "kitchen", "speaker_id": "alice", "resumed": "false",
		}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob/1", Fields: map[string]string{"text": "turn it down"}},
		{Kind: journal.KindSpeechDiscarded, Fields: map[string]string{
			"unspoken_text": "which room", "reason": "migrated",
		}},
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "migrated", "satellite": "kitchen"}},
		{Kind: journal.KindSessionOpened, Fields: map[string]string{
			"satellite": "office", "speaker_id": "alice", "resumed": "true",
		}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob/2", Fields: map[string]string{"text": "and the lights"}},
	} {
		if _, err := j.Append(ctx, "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}

	st, err := journal.Replay(ctx, store, "conv-1", journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !st.Open || st.Satellite != "office" || st.CloseReason != "" {
		t.Errorf("state = open %v on %q closed %q, want open on office with no close reason",
			st.Open, st.Satellite, st.CloseReason)
	}
	// What was cut off on the old device is still the conversation's context,
	// and still on the unheard side of it (SPEC §4.4).
	if want := []string{"turn it down", "and the lights"}; !reflect.DeepEqual(st.Heard, want) {
		t.Errorf("heard = %v, want %v", st.Heard, want)
	}
	if want := []string{"which room"}; !reflect.DeepEqual(st.Unspoken, want) {
		t.Errorf("unspoken = %v, want %v", st.Unspoken, want)
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

// verifies SPEC §4.4
func TestReduceKeepsDiscardedSpeechOutOfWhatWasHeard(t *testing.T) {
	e := journal.Event{
		Seq:  1,
		Kind: journal.KindSpeechDiscarded,
		Fields: map[string]string{
			"unspoken_text": "and the album came out in 1973",
			"reason":        "barge_in",
		},
	}

	got, err := journal.Reduce(journal.State{}, e)
	if err != nil {
		t.Fatalf("reduce: %v", err)
	}
	if len(got.Spoken) != 0 {
		t.Errorf("Spoken = %q; the user heard none of a discarded utterance", got.Spoken)
	}
	want := []string{"and the album came out in 1973"}
	if !reflect.DeepEqual(got.Unspoken, want) {
		t.Errorf("Unspoken = %q, want %q", got.Unspoken, want)
	}
}

// The first frame is when the answer was heard, not what was heard: the
// reducer must not count "one sec" twice because its start was journalled.
//
// verifies SPEC §11
func TestReduceTreatsTheFirstPlayedFrameAsTimingOnly(t *testing.T) {
	events := []journal.Event{
		{Seq: 1, Kind: journal.KindUtteranceTranscribed, AudioRef: "blob/1", Fields: map[string]string{"text": "play something by zeppelin"}},
		{Seq: 2, Kind: journal.KindSpeechStarted, Fields: map[string]string{"call_id": "call_2", "wait_ms": "1840"}},
		{Seq: 3, Kind: journal.KindSpeechSpoken, AudioRef: "blob/2", Fields: map[string]string{"text": "One sec.", "frames_played": "12800"}},
	}
	var (
		s   journal.State
		err error
	)
	for _, e := range events {
		if s, err = journal.Reduce(s, e); err != nil {
			t.Fatalf("reduce %s: %v", e.Kind, err)
		}
	}
	if want := []string{"One sec."}; !reflect.DeepEqual(s.Spoken, want) {
		t.Errorf("Spoken = %q, want %q", s.Spoken, want)
	}
	if s.LastSeq != 3 {
		t.Errorf("LastSeq = %d, want 3", s.LastSeq)
	}
}
