package erasure_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/erasure"
)

// saturday is the evening Alan deletes his sister's visit and Alice asks
// for her allergy to be forgotten.
var saturday = time.Date(2025, time.October, 11, 21, 4, 53, 123456000, time.UTC)

// evening is what a reviewer erased that Saturday, oldest first.
func evening() []erasure.Record {
	return []erasure.Record{
		{Kind: erasure.KindAudio, ConversationID: "conv-1930-living_room", Subject: "blob://mic/guest-ask blob://mic/guest-ask.ch1", Counted: "2 clips", At: saturday},
		{Kind: erasure.KindConversation, ConversationID: "conv-1930-living_room", Counted: "5 events, 1 clip", At: saturday.Add(time.Minute)},
		{Kind: erasure.KindMemory, ConversationID: "conv-0930-office", Subject: "m_a11e2961", Person: "alice", At: saturday.Add(2 * time.Minute)},
		{Kind: erasure.KindSummary, ConversationID: "conv-0930-office", Person: "alice", At: saturday.Add(3 * time.Minute)},
	}
}

// storeConformance runs against every Store: a behaviour MemStore has and
// PgStore lacks is a bug, not a backend detail.
var storeConformance = map[string]func(*testing.T, erasure.Store){
	"every field round-trips, newest first, each with its own id": func(t *testing.T, s erasure.Store) {
		ctx := context.Background()
		var ids []uint64
		for _, r := range evening() {
			id, err := s.Add(ctx, r)
			if err != nil {
				t.Fatalf("add %s: %v", r.Kind, err)
			}
			ids = append(ids, id)
		}
		got, err := s.List(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		want := evening()
		for i := range want {
			want[i].ID = ids[i]
		}
		slices.Reverse(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("listed %+v\nwant %+v", got, want)
		}
		if ids[0] == ids[1] {
			t.Errorf("two records share id %d", ids[0])
		}
	},

	"a record says what kind of thing went, which, and when": func(t *testing.T, s erasure.Store) {
		for _, bad := range []erasure.Record{
			{Kind: "photo", ConversationID: "conv-1930-living_room", At: saturday},
			{Kind: erasure.KindConversation, At: saturday},
			{Kind: erasure.KindConversation, ConversationID: "conv-1930-living_room"},
			{Kind: erasure.KindMemory, ConversationID: "conv-0930-office", At: saturday},
		} {
			if _, err := s.Add(context.Background(), bad); err == nil {
				t.Errorf("kept %+v", bad)
			}
		}
		if got, _ := s.List(context.Background()); len(got) != 0 {
			t.Errorf("a refused record was kept: %+v", got)
		}
	},
}

func runStoreConformance(t *testing.T, fresh func(*testing.T) erasure.Store) {
	t.Helper()
	for name, run := range storeConformance {
		t.Run(name, func(t *testing.T) { run(t, fresh(t)) })
	}
}

// verifies SPEC §8
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) erasure.Store { return erasure.NewMemStore() })
}
