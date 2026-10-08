package journal_test

import (
	"context"
	"maps"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// newStore builds an empty store. Postgres reuses one database, so a factory
// returns a store scoped to ids nothing else wrote.
type newStore func(t *testing.T) journal.Store

// storeConformance is one suite run against every Store. A behaviour MemStore
// has and PgStore lacks is a bug in one of them, not a backend detail.
var storeConformance = map[string]func(*testing.T, journal.Store){
	"unknown conversation is empty, not an error": func(t *testing.T, s journal.Store) {
		ctx := context.Background()

		seq, err := s.LastSeq(ctx, "conv-absent")
		if err != nil {
			t.Fatalf("last seq: %v", err)
		}
		if seq != 0 {
			t.Errorf("last seq = %d, want 0", seq)
		}

		events, err := s.Events(ctx, "conv-absent")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != 0 {
			t.Errorf("events = %v, want none", events)
		}
	},

	"every event field round-trips": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		want := journal.Event{
			Seq:            1,
			ConversationID: "conv-1",
			Kind:           journal.KindSpeechTruncated,
			Actor:          journal.Meta[journal.KindSpeechTruncated].Actor,
			At:             time.Date(2026, 10, 6, 12, 34, 56, 123456000, time.UTC),
			Speculative:    true,
			Versions:       versions(),
			AudioRef:       "blob/tts/42",
			Fields: map[string]string{
				"spoken_text":   "I found three",
				"unspoken_text": " albums by that artist",
				"frames_played": "6720",
			},
		}

		if err := s.Append(ctx, want); err != nil {
			t.Fatalf("append: %v", err)
		}
		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events, want 1", len(events))
		}
		assertEventEqual(t, events[0], want)
	},

	// verifies SPEC §8
	"empty STT and TTS slots round-trip as empty": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		// A deployment without a configured ear or voice stamps nothing in
		// these slots. Both backends must hand back exactly that, so a reader
		// never sees a backend's null where another gives an empty string.
		e := event("conv-1", 1)
		e.Versions = journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if got := events[0].Versions; got != e.Versions {
			t.Errorf("versions = %+v, want %+v", got, e.Versions)
		}
	},

	"absent fields round-trip as empty, not as a nil map": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		e := journal.Event{
			Seq: 1, ConversationID: "conv-1", Kind: journal.KindSessionClosed,
			At: time.Unix(1_760_000_000, 0).UTC(),
		}

		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		// Non-nil, not merely empty: the review UI and dataset export read this
		// column as SQL, where a null payload and an empty object differ.
		if events[0].Fields == nil {
			t.Error("fields came back nil, want an empty map")
		}
		if n := len(events[0].Fields); n != 0 {
			t.Errorf("fields = %v, want empty", events[0].Fields)
		}
	},

	// Postgres timestamptz is microsecond-resolution, so the seam is too. Both
	// stores must truncate identically or replay depends on the backend.
	"wall clock is stored to microsecond truth": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		e := event("conv-1", 1)
		e.At = time.Date(2026, 10, 6, 12, 34, 56, 123456789, time.UTC)
		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		want := time.Date(2026, 10, 6, 12, 34, 56, 123456000, time.UTC)
		if !events[0].At.Equal(want) {
			t.Errorf("at = %v, want %v truncated to microseconds", events[0].At.UTC(), want)
		}
	},

	"sequence must be exactly one past the last": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		cases := []uint64{0, 2, 7}

		for _, seq := range cases {
			err := s.Append(ctx, event("conv-1", seq))
			if err == nil {
				t.Errorf("append accepted seq %d into an empty log; want 1", seq)
			}
		}
		if err := s.Append(ctx, event("conv-1", 1)); err != nil {
			t.Fatalf("append seq 1: %v", err)
		}
		if err := s.Append(ctx, event("conv-1", 3)); err == nil {
			t.Error("append accepted a gap at seq 3")
		}
	},

	"a taken sequence cannot be rewritten": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		if err := s.Append(ctx, event("conv-1", 1)); err != nil {
			t.Fatalf("append: %v", err)
		}

		if err := s.Append(ctx, event("conv-1", 1)); err == nil {
			t.Fatal("append overwrote seq 1; the log is not append-only")
		}

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("got %d events, want the rejected write left 1", len(events))
		}
	},

	"last seq tracks the appended log": func(t *testing.T, s journal.Store) {
		ctx := context.Background()

		for seq := uint64(1); seq <= 4; seq++ {
			if err := s.Append(ctx, event("conv-1", seq)); err != nil {
				t.Fatalf("append %d: %v", seq, err)
			}
			got, err := s.LastSeq(ctx, "conv-1")
			if err != nil {
				t.Fatalf("last seq: %v", err)
			}
			if got != seq {
				t.Errorf("last seq = %d, want %d", got, seq)
			}
		}
	},

	"conversations are isolated and each sequence starts at one": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		for _, conv := range []string{"conv-1", "conv-2"} {
			for seq := uint64(1); seq <= 2; seq++ {
				if err := s.Append(ctx, event(conv, seq)); err != nil {
					t.Fatalf("append %s/%d: %v", conv, seq, err)
				}
			}
		}

		for _, conv := range []string{"conv-1", "conv-2"} {
			events, err := s.Events(ctx, conv)
			if err != nil {
				t.Fatalf("events %s: %v", conv, err)
			}
			if len(events) != 2 {
				t.Fatalf("%s: got %d events, want 2", conv, len(events))
			}
			for i, e := range events {
				if e.ConversationID != conv {
					t.Errorf("%s: event %d belongs to %s", conv, i, e.ConversationID)
				}
			}
		}
	},

	"events come back in sequence order": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		// Append out of wall-clock order: ordering is the sequence's job, and a
		// query without ORDER BY would pass only by luck.
		const n = 12
		for seq := uint64(1); seq <= n; seq++ {
			e := event("conv-1", seq)
			e.At = time.Unix(1_760_000_000+int64(n-seq), 0).UTC()
			if err := s.Append(ctx, e); err != nil {
				t.Fatalf("append %d: %v", seq, err)
			}
		}

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != n {
			t.Fatalf("got %d events, want %d", len(events), n)
		}
		for i, e := range events {
			if want := uint64(i + 1); e.Seq != want {
				t.Fatalf("position %d holds seq %d, want %d", i, e.Seq, want)
			}
		}
	},

	"a reader cannot mutate the log": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		if err := s.Append(ctx, event("conv-1", 1)); err != nil {
			t.Fatalf("append: %v", err)
		}

		first, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		first[0].Kind = "tampered"
		first[0].Fields["reason"] = "tampered"

		second, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if second[0].Kind != journal.KindSessionClosed {
			t.Errorf("kind = %q; a reader mutated the log", second[0].Kind)
		}
		if got := second[0].Fields["reason"]; got != "model_ended" {
			t.Errorf("reason = %q; a reader mutated the log", got)
		}
	},

	"a writer cannot mutate the log after appending": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		e := event("conv-1", 1)
		if err := s.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
		e.Fields["reason"] = "tampered"

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if got := events[0].Fields["reason"]; got != "model_ended" {
			t.Errorf("reason = %q; the writer still shared the log's map", got)
		}
	},

	// verifies SPEC §8
	"two writers racing on one sequence: exactly one wins": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		// Both read LastSeq 0 and both claim seq 1. MemStore never faces this;
		// Postgres must reject the loser in the database, not in Go.
		var (
			wg   sync.WaitGroup
			errs = make([]error, 2)
		)
		start := make(chan struct{})
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = s.Append(ctx, event("conv-1", 1))
			}()
		}
		close(start)
		wg.Wait()

		won := 0
		for _, err := range errs {
			if err == nil {
				won++
			}
		}
		if won != 1 {
			t.Errorf("%d of 2 writers claimed seq 1, want exactly 1 (errs: %v)", won, errs)
		}

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("log holds %d events, want 1", len(events))
		}
	},

	// verifies SPEC §8
	"concurrent writers leave a gapless log": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		// Each writer retries at the new LastSeq, which is how a real caller
		// resolves the loss. The log must end gapless with no duplicates.
		const writers = 8
		var wg sync.WaitGroup
		for range writers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					last, err := s.LastSeq(ctx, "conv-1")
					if err != nil {
						t.Errorf("last seq: %v", err)
						return
					}
					if err := s.Append(ctx, event("conv-1", last+1)); err == nil {
						return
					}
				}
			}()
		}
		wg.Wait()

		events, err := s.Events(ctx, "conv-1")
		if err != nil {
			t.Fatalf("events: %v", err)
		}
		if len(events) != writers {
			t.Fatalf("log holds %d events, want %d", len(events), writers)
		}
		for i, e := range events {
			if want := uint64(i + 1); e.Seq != want {
				t.Errorf("position %d holds seq %d, want %d", i, e.Seq, want)
			}
		}
	},

	// verifies SPEC §8
	"replay reads the same state from any backend": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		j := journal.New(s, journal.FixedClock(time.Unix(1_760_000_000, 0).UTC()), versions())
		for _, r := range conversationRecords() {
			if _, err := j.Append(ctx, "conv-1", r); err != nil {
				t.Fatalf("append %s: %v", r.Kind, err)
			}
		}

		got, err := journal.Replay(ctx, s, "conv-1", journal.Overrides{})
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		if !reflect.DeepEqual(got, wantReplayState()) {
			t.Errorf("state mismatch\n got: %+v\nwant: %+v", got, wantReplayState())
		}
	},
}

// event is a minimal valid entry; the sequence is what most cases exercise.
func event(conv string, seq uint64) journal.Event {
	return journal.Event{
		Seq: seq, ConversationID: conv, Kind: journal.KindSessionClosed,
		Actor:  journal.Meta[journal.KindSessionClosed].Actor,
		At:     time.Unix(1_760_000_000, 0).UTC(),
		Fields: map[string]string{"reason": "model_ended"},
	}
}

func assertEventEqual(t *testing.T, got, want journal.Event) {
	t.Helper()
	if got.Seq != want.Seq || got.ConversationID != want.ConversationID {
		t.Errorf("identity = %d/%s, want %d/%s", got.Seq, got.ConversationID, want.Seq, want.ConversationID)
	}
	if got.Kind != want.Kind || got.Actor != want.Actor {
		t.Errorf("kind/actor = %s/%s, want %s/%s", got.Kind, got.Actor, want.Kind, want.Actor)
	}
	if !got.At.Equal(want.At) {
		t.Errorf("at = %v, want %v", got.At, want.At)
	}
	if got.Speculative != want.Speculative {
		t.Errorf("speculative = %v, want %v", got.Speculative, want.Speculative)
	}
	if got.Versions != want.Versions {
		t.Errorf("versions = %+v, want %+v", got.Versions, want.Versions)
	}
	if got.AudioRef != want.AudioRef {
		t.Errorf("audio ref = %q, want %q", got.AudioRef, want.AudioRef)
	}
	if !maps.Equal(got.Fields, want.Fields) {
		t.Errorf("fields = %v, want %v", got.Fields, want.Fields)
	}
}

// runStoreConformance runs the whole suite against one backend.
func runStoreConformance(t *testing.T, open newStore) {
	t.Helper()
	for name, run := range storeConformance {
		t.Run(name, func(t *testing.T) { run(t, open(t)) })
	}
}

// verifies SPEC §8
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) journal.Store { return journal.NewMemStore() })
}
