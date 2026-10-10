package memory_test

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/memory"
	"github.com/teaganglenn/chorus/internal/session"
)

// topics is an embedding model small enough to reason about: one dimension
// per household topic, counting the topic's words, and one shared by every
// text so none is the zero vector. Two texts about the garage point the same
// way; the garage and the coffee do not.
type topics struct {
	mu    sync.Mutex
	err   error
	block bool
	texts []string
	// gate, when set, holds every call of more than one text until closed:
	// a GPU still loading the model.
	gate chan struct{}
}

var topicWords = []string{
	"garage", "code", "coffee", "bins", "birthday", "dentist", "thermostat",
	"piano", "car", "key", "peanut", "filter", "cat", "book", "router",
	"lights", "recycling", "phone", "alarm", "run", "plants", "library",
	"school", "passport", "dishwasher", "timer", "weather", "package", "oven",
}

func (e *topics) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.texts = append(e.texts, texts...)
	err, block, gate := e.err, e.block, e.gate
	e.mu.Unlock()
	if gate != nil && len(texts) > 1 {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		v := make([]float32, len(topicWords)+1)
		v[len(topicWords)] = 0.05
		for j, w := range topicWords {
			v[j] = float32(strings.Count(strings.ToLower(t), w))
		}
		out[i] = v
	}
	return out, nil
}

func (e *topics) EmbedModel() string { return "nomic-embed-text" }

func (e *topics) embedded() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.texts)
}

// teagansYear is more than a turn is told: the garage code Teagan asked to
// be remembered in August, and two dozen things remembered since, newest
// first from breakfast on Thursday.
func teagansYear() []memory.Memory {
	facts := []string{
		"Takes oat milk in coffee.",
		"The bins go out on Thursday night.",
		"Alice's birthday is the fourteenth of March.",
		"The dentist appointment is the second Tuesday of each month.",
		"The thermostat stays at 19 degrees at night.",
		"Piano lessons are Wednesdays at five.",
		"The car is due for its service in November.",
		"The spare key is under the blue planter.",
		"Alan is allergic to peanuts.",
		"The water filter gets changed every two months.",
		"The cat is fed at seven and at six.",
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
		"The furnace takes a 16 by 25 air filter.",
	}
	var out []memory.Memory
	for i, f := range facts {
		out = append(out, memory.Memory{
			ID: "m_" + strings.Repeat("0", 6) + string(rune('a'+i)), Person: "teagan", Fact: f,
			ConversationID: "conv-kitchen-1", CallID: "call_r1", At: breakfast.Add(-time.Duration(i) * 24 * time.Hour),
		})
	}
	return append(out, memory.Memory{
		ID: "m_9a7e4512", Person: "teagan", Fact: "The garage door code is 4512.",
		ConversationID: "conv-garage-0814", CallID: "call_r1", At: breakfast.Add(-60 * 24 * time.Hour),
	})
}

// teagansMonth is the past week's conversations and one from three weeks
// ago, when Teagan set the thermostat's night schedule.
func teagansMonth() []memory.Summary {
	return append(teagansWeek(), memory.Summary{
		ConversationID: "conv-hallway-0918", Person: "teagan",
		Text: "Teagan asked for the thermostat to drop to 19 degrees every night at ten; the assistant set the schedule.",
		At:   friday.Add(-21 * 24 * time.Hour),
	})
}

func recalled(t *testing.T, r session.Memories, words string) session.Recollection {
	t.Helper()
	got, err := r.Recall(context.Background(), session.Ask{
		Person: "teagan", ConversationID: "conv-kitchen-0815", Now: friday, Words: words,
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	return got
}

func facts(r session.Recollection) []string {
	var out []string
	for _, m := range r.Memories {
		out = append(out, m.Fact)
	}
	return out
}

func told(r session.Recollection) []string {
	out := []string{}
	for _, s := range r.Summaries {
		out = append(out, s.ConversationID)
	}
	return out
}

// Teagan has more remembered than a turn is told, and asks for the garage
// code, the oldest of it. Ranked, the code is recalled and the oldest of the
// rest makes room; newest-first, it never would be.
//
// verifies SPEC §5
func TestTheGarageCodeIsRecalledWhenItIsWhatWasAsked(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, teagansYear())
	const words = "what's the code for the garage"

	plain := recalled(t, memory.Recaller(store, memory.RecallConfig{}), words)
	if slices.Contains(facts(plain), "The garage door code is 4512.") || plain.RankedBy != "" {
		t.Fatalf("newest first recalled %q ranked by %q, want the code left out", facts(plain), plain.RankedBy)
	}

	got := recalled(t, memory.Recaller(store, memory.RecallConfig{Embedder: &topics{}}), words)
	if len(got.Memories) != memory.RecallLimit {
		t.Fatalf("recalled %d, want the limit of %d", len(got.Memories), memory.RecallLimit)
	}
	if got.RankedBy != "nomic-embed-text" {
		t.Errorf("ranked by %q, want the embedding model", got.RankedBy)
	}
	want := append(facts(plain)[:memory.RecallLimit-1:memory.RecallLimit-1], "The garage door code is 4512.")
	if !reflect.DeepEqual(facts(got), want) {
		t.Errorf("recalled %q,\nwant the newest nineteen and then the garage code, newest first", facts(got))
	}
}

// Teagan asks what the thermostat was set to at night. The conversation that
// set it was three weeks ago, past the week a turn is told newest-first;
// ranked, it is told, and yesterday's eggs still are.
//
// verifies SPEC §5
func TestAConversationFromWeeksAgoIsToldWhenItIsWhatWasAsked(t *testing.T) {
	store := memory.NewMemStore()
	keepAll(t, store, teagansMonth())
	r := memory.Recaller(store, memory.RecallConfig{Embedder: &topics{}})

	got := recalled(t, r, "what did we set the thermostat to at night")
	want := []string{"conv-garage-0812", "conv-kitchen-0731", "conv-office-0702", "conv-front-door-0611", "conv-hallway-0918"}
	if !reflect.DeepEqual(told(got), want) || got.RankedBy != "nomic-embed-text" {
		t.Errorf("told %q ranked by %q, want the week's newest four and the thermostat", told(got), got.RankedBy)
	}

	got = recalled(t, r, "what did I ask you yesterday")
	want = []string{"conv-garage-0812", "conv-kitchen-0731", "conv-office-0702", "conv-front-door-0611", "conv-kitchen-0530"}
	if !reflect.DeepEqual(told(got), want) {
		t.Errorf("asked about yesterday, told %q, want the newest five", told(got))
	}
}

// Alan has little remembered and talked once this month: everything fits,
// so nothing is embedded, and a conversation older than a week is told
// because nothing more relevant competes with it.
//
// verifies SPEC §5
func TestNothingIsRankedWhenEverythingFits(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, household())
	keepAll(t, store, []memory.Summary{{
		ConversationID: "conv-garage-0920", Person: "teagan",
		Text: "Teagan asked whether the garage door was closed; it was.", At: friday.Add(-18 * 24 * time.Hour),
	}})
	e := &topics{}
	got := recalled(t, memory.Recaller(store, memory.RecallConfig{Embedder: e}), "is the garage door closed")
	if len(got.Memories) != 3 || !reflect.DeepEqual(told(got), []string{"conv-garage-0920"}) || got.RankedBy != "" {
		t.Errorf("recalled %q and %q ranked by %q, want everything, unranked", facts(got), told(got), got.RankedBy)
	}
	if n := len(e.embedded()); n != 0 {
		t.Errorf("embedded %d texts with nothing to choose between", n)
	}
}

// What is remembered is embedded once; each turn after embeds only its
// words.
//
// verifies SPEC §5, §11
func TestATurnEmbedsOnlyItsWords(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, teagansYear())
	e := &topics{}
	r := memory.Recaller(store, memory.RecallConfig{Embedder: e})

	recalled(t, r, "what's the code for the garage")
	first := len(e.embedded())
	if first != len(teagansYear())+1 {
		t.Fatalf("first turn embedded %d texts, want the %d memories and the words", first, len(teagansYear()))
	}
	recalled(t, r, "when is the dentist")
	if got := e.embedded()[first:]; !reflect.DeepEqual(got, []string{"when is the dentist"}) {
		t.Errorf("second turn embedded %q, want only its words", got)
	}
}

// The embedding model is down, or too slow for the turn. The turn is told
// the newest, as it would be without one, and the daemon hears why.
//
// verifies SPEC §5, §7
func TestRankingThatFailsTellsTheNewest(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, teagansYear())
	keepAll(t, store, teagansMonth())
	want := recalled(t, memory.Recaller(store, memory.RecallConfig{}), "what's the code for the garage")

	for name, e := range map[string]*topics{
		"unreachable": {err: errors.New(`Post "http://ollama.lan:11434/api/embed": connection refused`)},
		"too slow":    {block: true},
	} {
		t.Run(name, func(t *testing.T) {
			var heard []string
			r := memory.Recaller(store, memory.RecallConfig{
				Embedder: e, Timeout: 20 * time.Millisecond,
				Failed: func(person string, err error) { heard = append(heard, person+": "+err.Error()) },
			})
			got := recalled(t, r, "what's the code for the garage")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("told %q and %q ranked by %q, want the newest, unranked", facts(got), told(got), got.RankedBy)
			}
			if len(heard) != 1 || !strings.HasPrefix(heard[0], "teagan: rank memories: ") {
				t.Errorf("the daemon heard %q, want why ranking failed for teagan", heard)
			}
		})
	}
}

// The first turn after a restart cannot wait for two dozen memories to be
// embedded on a GPU still loading the model, and is told the newest. The
// embedding carries on without it, and the next turn is ranked.
//
// verifies SPEC §5, §11
func TestATurnTooSoonForRankingLeavesTheNextOneRanked(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, teagansYear())
	e := &topics{gate: make(chan struct{})}
	r := memory.Recaller(store, memory.RecallConfig{Embedder: e, Timeout: 50 * time.Millisecond})

	first := recalled(t, r, "what's the code for the garage")
	if first.RankedBy != "" || slices.Contains(facts(first), "The garage door code is 4512.") {
		t.Fatalf("the first turn was told %q ranked by %q, want the newest", facts(first), first.RankedBy)
	}
	close(e.gate)
	got := recalled(t, r, "what's the code for the garage")
	if got.RankedBy != "nomic-embed-text" || !slices.Contains(facts(got), "The garage door code is 4512.") {
		t.Errorf("the next turn was told %q ranked by %q, want the code", facts(got), got.RankedBy)
	}
	// The first turn gave up before its words were embedded.
	if n := len(e.embedded()); n != len(teagansYear())+1 {
		t.Errorf("embedded %d texts, want what is kept once and the second turn's words", n)
	}
}
