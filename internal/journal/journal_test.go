package journal_test

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

func versions() journal.Versions {
	return journal.Versions{
		Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7",
		STT: "istupakov/parakeet-tdt-0.6b-v2-onnx", TTS: "kokoro/af_heart",
	}
}

// verifies SPEC §8
func TestAppendStampsMonotonicSequenceAndWallClock(t *testing.T) {
	clk := journal.FixedClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	j := journal.New(journal.NewMemStore(), clk, versions())

	for range 3 {
		if _, err := j.Append(context.Background(), "conv-1", journal.Record{
			Kind:   journal.KindSessionOpened,
			Fields: map[string]string{"satellite": "kitchen"},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	events, err := j.Events(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for i, e := range events {
		if want := uint64(i + 1); e.Seq != want {
			t.Errorf("event %d: seq = %d, want %d", i, e.Seq, want)
		}
		if !e.At.Equal(clk.Now()) {
			t.Errorf("event %d: at = %v, want %v", i, e.At, clk.Now())
		}
	}
}

// A conversation has more than one writer by design: the same person waking a
// second satellite inside the migration window resumes the same conversation,
// so a second session appends to the same log (SPEC §4.5). Append reads the
// last sequence number and then writes, and the store rejects a number that is
// not exactly one past the last, so an unserialised pair of writers loses an
// event rather than racing silently.
//
// verifies SPEC §8, §4.5
func TestConcurrentWritersShareOneSequence(t *testing.T) {
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), versions())

	const writers = 32
	errs := make(chan error, writers)
	var start sync.WaitGroup
	start.Add(1)
	for i := range writers {
		go func() {
			start.Wait()
			_, err := j.Append(context.Background(), "conv-1", journal.Record{
				Kind:   journal.KindToolResult,
				Fields: map[string]string{"call_id": "c" + strconv.Itoa(i), "outcome": "ok"},
			})
			errs <- err
		}()
	}
	start.Done()
	for range writers {
		if err := <-errs; err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	events, err := j.Events(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != writers {
		t.Fatalf("got %d events, want %d", len(events), writers)
	}
	// Gapless and unique: replay reduces in sequence order, so a repeated or
	// skipped number is a log it cannot read back.
	for i, e := range events {
		if want := uint64(i + 1); e.Seq != want {
			t.Errorf("event %d: seq = %d, want %d", i, e.Seq, want)
		}
	}
}

// stallStore holds one conversation's LastSeq inside the store, modelling a
// connection that has hung.
type stallStore struct {
	*journal.MemStore
	conv   string
	inside chan struct{} // closed once the stalled call is in the store
	freed  chan struct{} // closed to let it finish
	once   sync.Once
}

func (s *stallStore) LastSeq(ctx context.Context, conversationID string) (uint64, error) {
	if conversationID == s.conv {
		s.once.Do(func() { close(s.inside) })
		<-s.freed
	}
	return s.MemStore.LastSeq(ctx, conversationID)
}

// One stalled conversation may not stop another from recording. A session
// appends on a context with no cancellation and no deadline, so a lock shared
// across conversations turns one hung connection into a household-wide outage:
// nothing can be journalled, and the journal is the runtime (SPEC §8).
//
// verifies SPEC §8
func TestAStalledConversationDoesNotBlockAnother(t *testing.T) {
	store := &stallStore{
		MemStore: journal.NewMemStore(), conv: "conv-hung",
		inside: make(chan struct{}), freed: make(chan struct{}),
	}
	j := journal.New(store, journal.FixedClock(time.Unix(0, 0)), versions())

	rec := journal.Record{
		Kind:   journal.KindSessionOpened,
		Fields: map[string]string{"satellite": "kitchen"},
	}

	hung := make(chan error, 1)
	go func() {
		_, err := j.Append(context.Background(), "conv-hung", rec)
		hung <- err
	}()
	<-store.inside

	live := make(chan error, 1)
	go func() {
		_, err := j.Append(context.Background(), "conv-live", rec)
		live <- err
	}()
	select {
	case err := <-live:
		if err != nil {
			t.Fatalf("append: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a hung conversation blocked an unrelated one from recording")
	}

	close(store.freed)
	if err := <-hung; err != nil {
		t.Fatalf("append after the stall cleared: %v", err)
	}
}

// verifies SPEC §8
func TestAppendRejectsMalformedWrites(t *testing.T) {
	cases := map[string]struct {
		versions journal.Versions
		record   journal.Record
	}{
		"undeclared kind": {versions(), journal.Record{Kind: "speech_maybe_spoken"}},
		"missing required field": {versions(), journal.Record{
			Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c1"},
		}},
		"audio without a blob reference": {versions(), journal.Record{
			Kind: journal.KindUtteranceTranscribed, Fields: map[string]string{"text": "hello"},
		}},
		"versions required but incomplete": {journal.Versions{Model: "qwen3-32b@1"}, journal.Record{
			Kind: journal.KindWakeRejected, AudioRef: "blob/1", Fields: map[string]string{"reason": "no_speech"},
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), tc.versions)

			if _, err := j.Append(context.Background(), "conv-1", tc.record); err == nil {
				t.Fatalf("append accepted %s", name)
			}
		})
	}
}

// verifies SPEC §8
func TestAppendStampsTheVersionsInEffect(t *testing.T) {
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), versions())

	e, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind: journal.KindSpeechTruncated, AudioRef: "blob/1",
		Fields: map[string]string{"spoken_text": "I found", "unspoken_text": " three", "frames_played": "6720"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if e.Versions != versions() {
		t.Errorf("versions = %+v, want %+v", e.Versions, versions())
	}
}

// The STT and TTS slots attribute a transcript and a voice, not a completion.
// A journal built without them, as a test or a text-only deployment is, must
// still record what the model said: completeness guards the pair's
// attribution to a model, prompt and tool schema, and nothing else.
//
// verifies SPEC §8
func TestAppendRecordsACompletionWithoutAnSTTOrTTSVersion(t *testing.T) {
	v := versions()
	v.STT, v.TTS = "", ""
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), v)

	e, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind:   journal.KindModelCompleted,
		Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"},
	})
	if err != nil {
		t.Fatalf("append refused a completion for want of an STT or TTS version: %v", err)
	}
	if e.Versions.STT != "" || e.Versions.TTS != "" {
		t.Errorf("versions = %+v, want the empty slots kept empty", e.Versions)
	}
}
