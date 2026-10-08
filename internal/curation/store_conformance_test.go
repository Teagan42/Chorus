package curation_test

import (
	"context"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/curation"
)

// newStore builds an empty store. Postgres reuses one database, so its
// factory must hand back a clean table.
type newStore func(t *testing.T) curation.Store

// storeConformance is one suite run against every Store, like the journal's:
// a behaviour MemStore has and PgStore lacks is a bug, not a backend detail.
var storeConformance = map[string]func(*testing.T, curation.Store){
	"an unknown pair has no decision": func(t *testing.T, s curation.Store) {
		_, ok, err := s.Get(context.Background(), "conv-1/41")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if ok {
			t.Error("got a decision for a pair nobody reviewed")
		}
	},

	"every decision field round-trips": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		want := curation.Decision{
			PairID:         "conv-1/41",
			ConversationID: "conv-1",
			Status:         curation.StatusAccepted,
			Chosen:         "Dentist at nine. That's it.",
			Unfixed:        true,
			Reason:         "",
			Prev:           curation.StatusEdited,
			DecidedAt:      time.Date(2026, 10, 8, 22, 4, 53, 123456000, time.UTC),
		}
		if err := s.Put(ctx, want); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, ok, err := s.Get(ctx, want.PairID)
		if err != nil || !ok {
			t.Fatalf("get: ok=%v err=%v", ok, err)
		}
		if got != want {
			t.Errorf("decision = %+v, want %+v", got, want)
		}
	},

	"the last decision wins": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		d := decision("conv-1/41", curation.StatusAccepted)
		if err := s.Put(ctx, d); err != nil {
			t.Fatalf("put: %v", err)
		}
		d.Status, d.Reason = curation.StatusDiscarded, "Duplicate"
		d.DecidedAt = d.DecidedAt.Add(time.Minute)
		if err := s.Put(ctx, d); err != nil {
			t.Fatalf("put again: %v", err)
		}
		got, _, err := s.Get(ctx, d.PairID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got != d {
			t.Errorf("decision = %+v, want the second write %+v", got, d)
		}
	},

	"a conversation's decisions come back keyed by pair": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		mine := decision("conv-1/41", curation.StatusAccepted)
		other := decision("conv-2/7", curation.StatusEdited)
		other.ConversationID = "conv-2"
		for _, d := range []curation.Decision{mine, other} {
			if err := s.Put(ctx, d); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
		got, err := s.ForConversation(ctx, "conv-1")
		if err != nil {
			t.Fatalf("for conversation: %v", err)
		}
		if len(got) != 1 || got[mine.PairID] != mine {
			t.Errorf("decisions = %v, want only %+v", got, mine)
		}
	},

	"a decision without a pair id is refused": func(t *testing.T, s curation.Store) {
		err := s.Put(context.Background(), decision("", curation.StatusAccepted))
		if err == nil {
			t.Error("stored a decision no pair can claim")
		}
	},

	"a decision with an unknown prev status is refused": func(t *testing.T, s curation.Store) {
		d := decision("conv-1/41", curation.StatusAccepted)
		d.Prev = "exploded"
		if err := s.Put(context.Background(), d); err == nil {
			t.Error("stored a prev status undo could never restore")
		}
	},

	"a decision with an unknown status is refused": func(t *testing.T, s curation.Store) {
		err := s.Put(context.Background(), decision("conv-1/41", "unreviewed"))
		if err == nil {
			t.Error("stored a status the Curate flow never produces")
		}
	},

	"a deleted decision is gone, and deleting nothing is fine": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		if err := s.Delete(ctx, "conv-1/41"); err != nil {
			t.Fatalf("delete absent: %v", err)
		}
		if err := s.Put(ctx, decision("conv-1/41", curation.StatusAccepted)); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := s.Delete(ctx, "conv-1/41"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, ok, err := s.Get(ctx, "conv-1/41"); err != nil || ok {
			t.Errorf("after delete: ok=%v err=%v, want gone", ok, err)
		}
	},
}

// decision is a minimal valid write; its DecidedAt is within StoredClockResolution.
func decision(pairID string, status curation.Status) curation.Decision {
	return curation.Decision{
		PairID:         pairID,
		ConversationID: "conv-1",
		Status:         status,
		Chosen:         "chosen text",
		DecidedAt:      time.Unix(1_760_000_000, 0).UTC(),
	}
}

func runStoreConformance(t *testing.T, open newStore) {
	t.Helper()
	for name, run := range storeConformance {
		t.Run(name, func(t *testing.T) { run(t, open(t)) })
	}
}

// verifies SPEC §9.2
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) curation.Store { return curation.NewMemStore() })
}
