package memory_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/memory"
)

// alicesAllergy is what Alice asked the office to keep, and later wants gone.
func alicesAllergy() memory.Memory {
	return memory.Memory{ID: "m_a11e2961", Person: "alice", Fact: "Allergic to peanuts.", Shareable: true, ConversationID: "conv-office-2", CallID: "call_r1", At: breakfast.Add(3 * time.Hour)}
}

func kept(t *testing.T, s memory.Store) memory.Kept {
	t.Helper()
	k, ok := s.(memory.Kept)
	if !ok {
		t.Fatalf("%T cannot be read whole", s)
	}
	return k
}

func summaryKeys(ss []memory.Summary) []string {
	out := []string{}
	for _, s := range ss {
		out = append(out, s.Person+"/"+s.ConversationID)
	}
	return out
}

// keptConformance is what the review UI's memory screen reads and forgets
// (ADR-0070), against every Store.
var keptConformance = map[string]func(*testing.T, memory.Store){
	"a reviewer reads every memory, private ones too, by person then newest first": func(t *testing.T, s memory.Store) {
		rememberAll(t, s, append(household(), alicesAllergy()))
		got, err := kept(t, s).AllMemories(context.Background())
		if err != nil {
			t.Fatalf("all memories: %v", err)
		}
		want := []string{"m_a11e2961", "m_c0ffee42", "m_77d01b2e", "m_51ab0c3d", "m_3f9c2a10"}
		if !reflect.DeepEqual(ids(got), want) {
			t.Errorf("read %v, want %v", ids(got), want)
		}
		if len(got) > 0 && !reflect.DeepEqual(got[0], alicesAllergy()) {
			t.Errorf("round-tripped %+v, want %+v", got[0], alicesAllergy())
		}
	},

	"a reviewer reads every summary, by person then newest first": func(t *testing.T, s memory.Store) {
		keepAll(t, s, teagansWeek())
		got, err := kept(t, s).AllSummaries(context.Background())
		if err != nil {
			t.Fatalf("all summaries: %v", err)
		}
		want := []string{
			"alan/conv-living-room-0820", "alan/conv-garage-0812",
			"teagan/conv-garage-0812", "teagan/conv-kitchen-0731", "teagan/conv-office-0702",
			"teagan/conv-front-door-0611", "teagan/conv-kitchen-0530",
		}
		if !reflect.DeepEqual(summaryKeys(got), want) {
			t.Errorf("read %v, want %v", summaryKeys(got), want)
		}
	},

	"forgetting one person's summary leaves the others in it": func(t *testing.T, s memory.Store) {
		keepAll(t, s, teagansWeek())
		k := kept(t, s)
		ok, err := k.ForgetSummary(context.Background(), "conv-garage-0812", "alan")
		if err != nil || !ok {
			t.Fatalf("forget alan's garage = %v, %v", ok, err)
		}
		if ok, _ := k.ForgetSummary(context.Background(), "conv-garage-0812", "alan"); ok {
			t.Error("forgot alan's garage twice")
		}
		got, _ := k.AllSummaries(context.Background())
		if want := []string{"alan/conv-living-room-0820"}; !reflect.DeepEqual(summaryKeys(got)[:1], want) {
			t.Errorf("alan keeps %v", summaryKeys(got))
		}
		if len(got) != 6 {
			t.Errorf("kept %d summaries after one was forgotten, want 6", len(got))
		}
	},

	"a deleted conversation's summaries go for everyone in it": func(t *testing.T, s memory.Store) {
		keepAll(t, s, teagansWeek())
		k := kept(t, s)
		if err := k.ForgetSummaries(context.Background(), "conv-garage-0812"); err != nil {
			t.Fatal(err)
		}
		got, _ := k.AllSummaries(context.Background())
		for _, sum := range got {
			if sum.ConversationID == "conv-garage-0812" {
				t.Errorf("%s still keeps the garage", sum.Person)
			}
		}
		if len(got) != 5 {
			t.Errorf("kept %d summaries, want 5", len(got))
		}
	},
}

func init() {
	for name, run := range keptConformance {
		storeConformance[name] = run
	}
}
