package household_test

import (
	"context"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/household"
	sess "github.com/teagan42/chorus/internal/session"
)

func run(t *testing.T, m *household.Model, prompt, conv, text string) rerun.Take {
	t.Helper()
	eng, _, err := m.Engines("qwen3-32b@1", prompt)
	if err != nil {
		t.Fatal(err)
	}
	take, err := rerun.Run(context.Background(), eng, conv, rerun.Turn{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return take
}

// Under the prompt the day ran with, the model is deterministic: every turn
// of every conversation comes back as recorded.
//
// verifies SPEC §9.2
func TestModelReproducesEveryRecordedTurnUnderTheDefaultPrompt(t *testing.T) {
	ctx := context.Background()
	store, err := household.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := household.NewModel(store)
	_, v, _ := m.Engines("qwen3-32b@1", ollama.DefaultPrompt)
	if v != household.Versions() {
		t.Errorf("default prompt runs as %+v, want the day's %+v", v, household.Versions())
	}
	var turns int
	ids, _ := store.Conversations(ctx)
	for _, id := range ids {
		events, _ := store.Events(ctx, id)
		ts, err := rerun.Turns(events)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range ts {
			turns++
			if c := rerun.Compare(turn.Recorded, run(t, m, ollama.DefaultPrompt, id, turn.Text)); c.Speech || c.Calls {
				t.Errorf("%s #%d %q changed under the default prompt: %+v", id, turn.Seq, turn.Text, c)
			}
		}
	}
	if turns != 15 {
		t.Errorf("%d turns in the day, want 15", turns)
	}
}

// Told to be brief, it leads with the count and offers the first; turns the
// edit has nothing to say about still answer as recorded.
//
// verifies SPEC §9.2
func TestModelAnswersBrieflyUnderAnEditedPrompt(t *testing.T) {
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := household.NewModel(store)
	edited := ollama.DefaultPrompt + "\n\nWhen there are several results, say how many, offer the first, and stop."
	if _, v, _ := m.Engines("qwen3-32b@1", edited); v.Prompt != "sys@edited" {
		t.Errorf("an edited prompt runs as %q, want sys@edited", v.Prompt)
	}
	zep := run(t, m, edited, household.ConvZeppelin, "play something by zeppelin")
	if zep.Said() != "I found three albums. Want Led Zeppelin one?" || len(zep.Calls) != 1 || zep.Calls[0].Tool != "media_search" {
		t.Errorf("zeppelin under the edit = %+v", zep)
	}
	list := run(t, m, edited, household.ConvList, "add oat milk to the shopping list")
	if list.Said() != "Added oat milk." || list.Calls[0].Tool != "ha_call_service" {
		t.Errorf("the shopping list changed under an edit about results: %+v", list)
	}
}

// A turn the household never said is refused, not invented.
func TestModelRefusesATurnItNeverHeard(t *testing.T) {
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _ := household.NewModel(store).Engines("qwen3-32b@1", ollama.DefaultPrompt)
	_, err = eng.Turn(context.Background(), sess.Input{ConversationID: household.ConvGarage, Speaker: "teagan", Text: "open the garage"})
	if err == nil || !strings.Contains(err.Error(), `never heard "open the garage"`) {
		t.Errorf("err = %v, want a refusal naming the turn", err)
	}
}
