package journal_test

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// Retention deletes from the log and reads its tail (ADR-0065). Both stores
// join the conformance suite, so the Postgres delete path runs under
// `task test:db` against exactly what MemStore does here.
func init() {
	maps.Copy(storeConformance, retentionConformance)
}

var retentionConformance = map[string]func(*testing.T, journal.Store){
	// verifies SPEC §8
	"deleting events keeps the rest and the next seq": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		appendN(t, s, "device:kitchen", 5)

		if err := deleter(t, s).DeleteEvents(ctx, "device:kitchen", []uint64{1, 2, 4}); err != nil {
			t.Fatalf("delete events: %v", err)
		}
		if got := seqsOf(t, s, "device:kitchen"); !slices.Equal(got, []uint64{3, 5}) {
			t.Errorf("seqs = %v, want [3 5]", got)
		}
		last, err := s.LastSeq(ctx, "device:kitchen")
		if err != nil || last != 5 {
			t.Fatalf("last seq = %d, %v; want 5", last, err)
		}
		// A deleted seq is not handed out again: a verdict keyed by it would
		// land on a different wake.
		if err := s.Append(ctx, event("device:kitchen", 4)); err == nil {
			t.Error("appending a deleted seq succeeded")
		}
		if err := s.Append(ctx, event("device:kitchen", 6)); err != nil {
			t.Errorf("append after delete: %v", err)
		}
	},

	// verifies SPEC §8
	"the last event is never deleted, so a log's seq never rewinds": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		appendN(t, s, journal.HouseTimers, 3)

		if err := deleter(t, s).DeleteEvents(ctx, journal.HouseTimers, []uint64{1, 2, 3}); err != nil {
			t.Fatalf("delete events: %v", err)
		}
		if got := seqsOf(t, s, journal.HouseTimers); !slices.Equal(got, []uint64{3}) {
			t.Errorf("seqs = %v, want [3]", got)
		}
		if err := s.Append(ctx, event(journal.HouseTimers, 4)); err != nil {
			t.Errorf("append after delete: %v", err)
		}
	},

	// verifies SPEC §8
	"deleting a log removes it whole and from the listing": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		appendN(t, s, "conv-old", 3)
		appendN(t, s, "conv-kept", 1)

		if err := deleter(t, s).DeleteLog(ctx, "conv-old"); err != nil {
			t.Fatalf("delete log: %v", err)
		}
		if got := seqsOf(t, s, "conv-old"); len(got) != 0 {
			t.Errorf("seqs = %v, want none", got)
		}
		ids, err := lister(t, s).Conversations(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(ids, []string{"conv-kept"}) {
			t.Errorf("conversations = %v, want [conv-kept]", ids)
		}
	},

	// verifies SPEC §8
	"events after a seq are the log's tail, in order": func(t *testing.T, s journal.Store) {
		ctx := context.Background()
		appendN(t, s, journal.HouseTimers, 5)
		r := ranger(t, s)

		tail, err := r.EventsAfter(ctx, journal.HouseTimers, 3)
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(tail); !slices.Equal(got, []uint64{4, 5}) {
			t.Errorf("after 3 = %v, want [4 5]", got)
		}
		all, err := r.EventsAfter(ctx, journal.HouseTimers, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := seqs(all); !slices.Equal(got, []uint64{1, 2, 3, 4, 5}) {
			t.Errorf("after 0 = %v, want the whole log", got)
		}
		none, err := r.EventsAfter(ctx, journal.HouseTimers, 5)
		if err != nil || len(none) != 0 {
			t.Errorf("after the last = %v, %v; want none", seqs(none), err)
		}
	},
}

func appendN(t *testing.T, s journal.Store, conv string, n uint64) {
	t.Helper()
	for seq := uint64(1); seq <= n; seq++ {
		if err := s.Append(context.Background(), event(conv, seq)); err != nil {
			t.Fatalf("append %s %d: %v", conv, seq, err)
		}
	}
}

func seqsOf(t *testing.T, s journal.Store, conv string) []uint64 {
	t.Helper()
	events, err := s.Events(context.Background(), conv)
	if err != nil {
		t.Fatalf("events %s: %v", conv, err)
	}
	return seqs(events)
}

func seqs(events []journal.Event) []uint64 {
	out := make([]uint64, 0, len(events))
	for _, e := range events {
		out = append(out, e.Seq)
	}
	return out
}

func deleter(t *testing.T, s journal.Store) journal.Deleter {
	t.Helper()
	d, ok := s.(journal.Deleter)
	if !ok {
		t.Fatalf("%T cannot delete", s)
	}
	return d
}

func ranger(t *testing.T, s journal.Store) journal.Ranger {
	t.Helper()
	r, ok := s.(journal.Ranger)
	if !ok {
		t.Fatalf("%T cannot read a tail", s)
	}
	return r
}
