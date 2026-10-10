//go:build models

package ollama_test

import (
	"context"
	"flag"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/session"
)

var embedModel = flag.String("embed-model", "nomic-embed-text", "embedding model recall ranks with")

// teagansHousehold is two dozen things Teagan has asked to be remembered,
// newest first: more than a turn is told, and nothing about the questions
// below.
var teagansHousehold = []string{
	"Takes oat milk in coffee.",
	"The bins go out on Thursday night.",
	"Alice's birthday is the fourteenth of March.",
	"The dentist appointment is the second Tuesday of each month.",
	"The thermostat stays at 19 degrees at night.",
	"Piano lessons are Wednesdays at five.",
	"The car is due for its service in November.",
	"Book club meets the first Sunday of the month.",
	"The router password is on the back of the router.",
	"Prefers the hallway lights at half brightness.",
	"The recycling goes out every other Wednesday.",
	"Grandma's phone number is in the kitchen drawer.",
	"The smoke alarm batteries were changed in September.",
	"Goes for a run on Saturday mornings.",
	"The porch plants get watered on Mondays.",
	"The library books are due on the twentieth.",
	"The kids' school pickup is at quarter past three.",
	"Teagan's passport expires next June.",
	"The dishwasher salt is under the sink.",
	"The cat is fed at seven and at six.",
	"The water filter gets changed every two months.",
	"Takes the 7:40 train on weekdays.",
}

// recallFor stores the household with fact as its oldest memory, and
// recalls for words with the real embedding model.
func recallFor(t *testing.T, fact, words string) session.Recollection {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -ollama-url")
	}
	emb, err := ollama.NewEmbedder(ollama.EmbedConfig{BaseURL: *endpoint, Model: *embedModel})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	store := memory.NewMemStore()
	now := time.Date(2026, 10, 9, 8, 15, 0, 0, time.UTC)
	for i, f := range append(slices.Clone(teagansHousehold), fact) {
		if err := store.Remember(context.Background(), memory.Memory{
			ID: fmt.Sprintf("m_%08x", i), Person: "teagan", Fact: f,
			At: now.Add(-time.Duration(i+1) * 24 * time.Hour),
		}); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
	// The cold load is this test's to pay, not the turn's bound.
	r := memory.Recaller(store, memory.RecallConfig{
		Embedder: emb, Timeout: turnBudget,
		Failed: func(_ string, err error) { t.Errorf("%s ranking failed: %v", *embedModel, err) },
	})
	got, err := r.Recall(context.Background(), session.Ask{Person: "teagan", ConversationID: "models-test", Now: now, Words: words})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	return got
}

func recalls(r session.Recollection, fact string) bool {
	return slices.ContainsFunc(r.Memories, func(m journal.Memory) bool { return m.Fact == fact })
}

// A real embedding model brings the oldest memory back when it is what was
// asked about, in words that share little with how it was remembered.
//
// verifies SPEC §5
func TestARealEmbeddingModelRecallsWhatWasAskedAbout(t *testing.T) {
	cases := []struct{ fact, words string }{
		{"The garage door code is 4512.", "how do I get into the garage"},
		{"Alan is allergic to peanuts.", "can Alan have the satay"},
		{"The spare key is under the blue planter.", "I'm locked out, where's the other key"},
		{"The furnace takes a 16 by 25 air filter.", "what size filter does the heater need"},
	}
	for _, c := range cases {
		got := recallFor(t, c.fact, c.words)
		if got.RankedBy != *embedModel || len(got.Memories) != memory.RecallLimit {
			t.Fatalf("recalled %d ranked by %q, want %d ranked by %s", len(got.Memories), got.RankedBy, memory.RecallLimit, *embedModel)
		}
		if !recalls(got, c.fact) {
			t.Errorf("%s: %q did not bring back %q", *embedModel, c.words, c.fact)
		}
	}
}

// End to end on real models: the code is chosen for the question, and the
// turn model answers with it.
//
// verifies SPEC §5
func TestARealModelAnswersWithTheMemoryChosenForTheQuestion(t *testing.T) {
	got := recallFor(t, "The garage door code is 4512.", "what's the code for the garage")
	if !recalls(got, "The garage door code is 4512.") {
		t.Fatalf("%s did not choose the garage code", *embedModel)
	}
	acts := ask(t, engine(t), session.Input{
		ConversationID: "models-test", Speaker: "teagan", Text: "What's the code for the garage?", Memories: got.Memories,
	})
	// Written for speech, the code may come out in words.
	said := spoken(acts)
	run := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, said)
	if !strings.Contains(run, "4512") && !strings.Contains(run, "fourfiveonetwo") {
		t.Errorf("%s said %q, want the code: %v", *model, said, describe(acts))
	}
}
