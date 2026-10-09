package memory_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/memory"
)

// breakfast is when Teagan told the kitchen about the oat milk.
var breakfast = time.Date(2026, 10, 8, 7, 42, 13, 123456000, time.UTC)

// The household's memories: Teagan's own, Teagan's and Alice's shared ones,
// and Alice's surprise for Teagan, which is nobody else's business.
func household() []memory.Memory {
	return []memory.Memory{
		{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee.", ConversationID: "conv-kitchen-1", CallID: "call_r1", At: breakfast},
		{ID: "m_51ab0c3d", Person: "teagan", Fact: "The bins go out on Thursday night.", Shareable: true, ConversationID: "conv-kitchen-1", CallID: "call_r2", At: breakfast.Add(time.Minute)},
		{ID: "m_77d01b2e", Person: "alice", Fact: "The guest wifi password is on the fridge.", Shareable: true, ConversationID: "conv-office-1", CallID: "call_r1", At: breakfast.Add(time.Hour)},
		{ID: "m_c0ffee42", Person: "alice", Fact: "Teagan's surprise party is Saturday at the Lantern.", ConversationID: "conv-office-1", CallID: "call_r2", At: breakfast.Add(2 * time.Hour)},
	}
}

func rememberAll(t *testing.T, s memory.Store, ms []memory.Memory) {
	t.Helper()
	for _, m := range ms {
		if err := s.Remember(context.Background(), m); err != nil {
			t.Fatalf("remember %s: %v", m.ID, err)
		}
	}
}

func ids(ms []memory.Memory) []string {
	out := []string{}
	for _, m := range ms {
		out = append(out, m.ID)
	}
	return out
}

// storeConformance runs against every Store: a behaviour MemStore has and
// PgStore lacks is a bug, not a backend detail.
var storeConformance = map[string]func(*testing.T, memory.Store){
	"every field round-trips": func(t *testing.T, s memory.Store) {
		want := household()[0]
		rememberAll(t, s, []memory.Memory{want})
		got, err := s.Recall(context.Background(), "teagan", memory.RecallLimit)
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Errorf("recalled %+v, want %+v", got, want)
		}
	},

	"a person recalls their own and what others shared, newest first": func(t *testing.T, s memory.Store) {
		rememberAll(t, s, household())
		cases := map[string][]string{
			// Alice's surprise stays hers.
			"teagan": {"m_77d01b2e", "m_51ab0c3d", "m_3f9c2a10"},
			// Teagan's coffee is not Alice's to be told.
			"alice": {"m_c0ffee42", "m_77d01b2e", "m_51ab0c3d"},
			// Alan has nothing of his own yet, but lives here.
			"alan": {"m_77d01b2e", "m_51ab0c3d"},
		}
		for person, want := range cases {
			got, err := s.Recall(context.Background(), person, memory.RecallLimit)
			if err != nil {
				t.Fatalf("recall %s: %v", person, err)
			}
			if !reflect.DeepEqual(ids(got), want) {
				t.Errorf("%s recalls %v, want %v", person, ids(got), want)
			}
		}
	},

	"the limit keeps the newest": func(t *testing.T, s memory.Store) {
		rememberAll(t, s, household())
		got, err := s.Recall(context.Background(), "teagan", 2)
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		if want := []string{"m_77d01b2e", "m_51ab0c3d"}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("recalled %v, want %v", ids(got), want)
		}
	},

	"memories made in the same instant recall in id order": func(t *testing.T, s memory.Store) {
		rememberAll(t, s, []memory.Memory{
			{ID: "m_b2000000", Person: "teagan", Fact: "Prefers the porch light off after ten.", ConversationID: "conv-1", CallID: "call_r2", At: breakfast},
			{ID: "m_a1000000", Person: "teagan", Fact: "Allergic to cats.", ConversationID: "conv-1", CallID: "call_r1", At: breakfast},
		})
		got, err := s.Recall(context.Background(), "teagan", memory.RecallLimit)
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		if want := []string{"m_a1000000", "m_b2000000"}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("recalled %v, want %v", ids(got), want)
		}
	},

	"a person forgets only their own": func(t *testing.T, s memory.Store) {
		ctx := context.Background()
		rememberAll(t, s, household())
		// The wifi password is shared with Teagan, not Teagan's to erase.
		if gone, err := s.Forget(ctx, "teagan", "m_77d01b2e"); err != nil || gone {
			t.Errorf("teagan forgetting alice's password: gone=%v err=%v, want refused", gone, err)
		}
		if gone, err := s.Forget(ctx, "teagan", "m_3f9c2a10"); err != nil || !gone {
			t.Errorf("teagan forgetting the oat milk: gone=%v err=%v, want gone", gone, err)
		}
		if gone, err := s.Forget(ctx, "teagan", "m_3f9c2a10"); err != nil || gone {
			t.Errorf("forgetting it twice: gone=%v err=%v, want nothing left to forget", gone, err)
		}
		got, err := s.Recall(ctx, "teagan", memory.RecallLimit)
		if err != nil {
			t.Fatalf("recall: %v", err)
		}
		if want := []string{"m_77d01b2e", "m_51ab0c3d"}; !reflect.DeepEqual(ids(got), want) {
			t.Errorf("teagan recalls %v after forgetting, want %v", ids(got), want)
		}
	},

	"a taken id is refused": func(t *testing.T, s memory.Store) {
		first := household()[0]
		rememberAll(t, s, []memory.Memory{first})
		again := household()[1]
		again.ID = first.ID
		if err := s.Remember(context.Background(), again); err == nil {
			t.Error("a second memory took the oat milk's id")
		}
	},

	"a memory needs a person and a fact": func(t *testing.T, s memory.Store) {
		nobody := household()[0]
		nobody.Person = ""
		blank := household()[1]
		blank.Fact = "   "
		for _, m := range []memory.Memory{nobody, blank} {
			if err := s.Remember(context.Background(), m); err == nil {
				t.Errorf("remembered %+v", m)
			}
		}
	},
}

func runStoreConformance(t *testing.T, fresh func(*testing.T) memory.Store) {
	t.Helper()
	for name, run := range storeConformance {
		t.Run(name, func(t *testing.T) { run(t, fresh(t)) })
	}
}

// verifies SPEC §5
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) memory.Store { return memory.NewMemStore() })
}
