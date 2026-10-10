package memory_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/memory"
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

// friday is when the household's week of conversations ends: Teagan asks
// "what did I ask yesterday" from the kitchen.
var friday = time.Date(2026, 10, 9, 8, 15, 0, 0, time.UTC)

// teagansWeek is what Teagan talked about this past week and a bit, newest
// first, and two conversations Alan was in.
func teagansWeek() []memory.Summary {
	return []memory.Summary{
		{ConversationID: "conv-garage-0812", Person: "teagan", Text: "Teagan asked whether the garage door was closed; it was open, and Chorus closed it.", At: friday.Add(-14 * time.Hour)},
		{ConversationID: "conv-kitchen-0731", Person: "teagan", Text: "Teagan set a ten minute timer for the eggs.", At: friday.Add(-25 * time.Hour)},
		{ConversationID: "conv-office-0702", Person: "teagan", Text: "Teagan asked for the weather in Portland; rain all afternoon.", At: friday.Add(-3 * 24 * time.Hour)},
		{ConversationID: "conv-front-door-0611", Person: "teagan", Text: "Teagan asked whether the package had come; it had not.", At: friday.Add(-6 * 24 * time.Hour)},
		{ConversationID: "conv-kitchen-0530", Person: "teagan", Text: "Teagan asked for the oven to preheat to 200.", At: friday.Add(-9 * 24 * time.Hour)},
		{ConversationID: "conv-garage-0812", Person: "alan", Text: "Alan asked about the garage door with Teagan; it was closed.", At: friday.Add(-14 * time.Hour)},
		{ConversationID: "conv-living-room-0820", Person: "alan", Text: "Alan turned the living room lights down.", At: friday.Add(-13 * time.Hour)},
	}
}

func keepAll(t *testing.T, s memory.Store, ss []memory.Summary) {
	t.Helper()
	for _, sum := range ss {
		if err := s.Summarized(context.Background(), sum); err != nil {
			t.Fatalf("keep %s for %s: %v", sum.ConversationID, sum.Person, err)
		}
	}
}

func conversations(ss []memory.Summary) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.ConversationID)
	}
	return out
}

var summaryConformance = map[string]func(*testing.T, memory.Store){
	"a person recalls only their own conversations, newest first": func(t *testing.T, s memory.Store) {
		keepAll(t, s, teagansWeek())
		got, err := s.Summaries(context.Background(), "teagan", "conv-kitchen-0815", friday.Add(-memory.SummaryWindow), memory.SummaryLimit)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		// Last week's oven is too old; Alan's lights were never Teagan's.
		want := []string{"conv-garage-0812", "conv-kitchen-0731", "conv-office-0702", "conv-front-door-0611"}
		if !reflect.DeepEqual(conversations(got), want) {
			t.Errorf("teagan recalls %v, want %v", conversations(got), want)
		}
		if len(got) > 0 && !reflect.DeepEqual(got[0], teagansWeek()[0]) {
			t.Errorf("round-tripped %+v, want %+v", got[0], teagansWeek()[0])
		}

		alan, err := s.Summaries(context.Background(), "alan", "", friday.Add(-memory.SummaryWindow), memory.SummaryLimit)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		if want := []string{"conv-living-room-0820", "conv-garage-0812"}; !reflect.DeepEqual(conversations(alan), want) {
			t.Errorf("alan recalls %v, want %v", conversations(alan), want)
		}
	},

	"the conversation in progress is not recalled to itself": func(t *testing.T, s memory.Store) {
		keepAll(t, s, teagansWeek())
		got, err := s.Summaries(context.Background(), "teagan", "conv-garage-0812", friday.Add(-memory.SummaryWindow), 1)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		if want := []string{"conv-kitchen-0731"}; !reflect.DeepEqual(conversations(got), want) {
			t.Errorf("recalled %v, want %v", conversations(got), want)
		}
	},

	"a resumed conversation's newer summary replaces the older, never the reverse": func(t *testing.T, s memory.Store) {
		first := teagansWeek()[0]
		resumed := first
		resumed.Text = "Teagan asked whether the garage door was closed; Chorus closed it, then turned the garage lights off."
		resumed.At = first.At.Add(4 * time.Minute)
		// The resumed conversation's summary landed first, and the slower
		// one from before the resume lands after it.
		keepAll(t, s, []memory.Summary{resumed, first})
		got, err := s.Summaries(context.Background(), "teagan", "", friday.Add(-memory.SummaryWindow), memory.SummaryLimit)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		if len(got) != 1 || !reflect.DeepEqual(got[0], resumed) {
			t.Errorf("kept %+v, want only the resumed conversation's", got)
		}
	},

	"keeping a summary prunes the person's month-old ones": func(t *testing.T, s memory.Store) {
		garage := teagansWeek()[0]
		monthBefore := garage.At.Add(-memory.SummaryKeep - time.Hour)
		old := memory.Summary{ConversationID: "conv-kitchen-0901", Person: "teagan", Text: "Teagan asked for a grocery list.", At: monthBefore}
		alans := memory.Summary{ConversationID: "conv-garage-0901", Person: "alan", Text: "Alan asked when the bins go out.", At: monthBefore}
		keepAll(t, s, []memory.Summary{old, alans, garage})
		ever := friday.Add(-365 * 24 * time.Hour)
		got, err := s.Summaries(context.Background(), "teagan", "", ever, memory.SummaryLimit)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		if want := []string{"conv-garage-0812"}; !reflect.DeepEqual(conversations(got), want) {
			t.Errorf("teagan keeps %v, want the month-old one pruned", conversations(got))
		}
		// Teagan's summary is no reason to prune Alan's.
		got, err = s.Summaries(context.Background(), "alan", "", ever, memory.SummaryLimit)
		if err != nil {
			t.Fatalf("summaries: %v", err)
		}
		if want := []string{"conv-garage-0901"}; !reflect.DeepEqual(conversations(got), want) {
			t.Errorf("alan keeps %v, want his untouched until he keeps another", conversations(got))
		}
	},

	"a summary needs a conversation, a person and some words": func(t *testing.T, s memory.Store) {
		for _, bad := range []memory.Summary{
			{Person: "teagan", Text: "Teagan set a timer.", At: friday},
			{ConversationID: "conv-kitchen-0731", Text: "Somebody set a timer.", At: friday},
			{ConversationID: "conv-kitchen-0731", Person: "teagan", Text: "  ", At: friday},
		} {
			if err := s.Summarized(context.Background(), bad); err == nil {
				t.Errorf("kept %+v", bad)
			}
		}
	},
}

func init() {
	for name, run := range summaryConformance {
		storeConformance[name] = run
	}
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
