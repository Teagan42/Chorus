package household_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/household"
	sess "github.com/teagan42/chorus/internal/session"
)

func run(t *testing.T, m *household.Model, prompt, conv, text string) rerun.Take {
	t.Helper()
	eng, _, err := m.Engines("qwen3-32b@1", prompt, registry.Offered())
	if err != nil {
		t.Fatal(err)
	}
	take, err := rerun.Run(context.Background(), eng, conv, rerun.Turn{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return take
}

// Under the prompt the day ran with and the tools chorusd offers, the model
// is deterministic: every turn of every conversation comes back as recorded,
// but for the search the registry no longer offers: each turn that searched
// for music says it cannot, and calls nothing, not even the playlist its
// search would have found.
//
// verifies SPEC §9.2, §14
func TestModelReproducesEveryRecordedTurnUnderTheDefaultPrompt(t *testing.T) {
	ctx := context.Background()
	store, err := household.Journal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := household.NewModel(store)
	_, v, _ := m.Engines("qwen3-32b@1", ollama.DefaultPrompt, registry.Offered())
	if want := (journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@8"}); v != want {
		t.Errorf("the default prompt and tools run as %+v, want the day's prompt and the registry's %+v", v, want)
	}
	var turns, searched int
	ids, _ := store.Conversations(ctx)
	for _, id := range ids {
		events, _ := store.Events(ctx, id)
		ts, err := rerun.Turns(events)
		if err != nil {
			t.Fatal(err)
		}
		for _, turn := range ts {
			turns++
			take := run(t, m, ollama.DefaultPrompt, id, turn.Text)
			if slices.ContainsFunc(turn.Recorded.Calls, func(c rerun.Call) bool { return c.Tool == "media_search" }) {
				searched++
				if len(take.Calls) != 0 || !strings.Contains(take.Said(), "I can't search for music yet") {
					t.Errorf("%s #%d %q offered no search = %+v, want it to say it cannot search", id, turn.Seq, turn.Text, take)
				}
				continue
			}
			if c := rerun.Compare(turn.Recorded, take); c.Speech || c.Calls {
				t.Errorf("%s #%d %q changed under the default prompt: %+v", id, turn.Seq, turn.Text, c)
			}
		}
	}
	if turns != 17 || searched != 3 {
		t.Errorf("%d turns in the day, %d of them searched; want 17 and Zeppelin, jazz and quieter", turns, searched)
	}
}

// Told to be brief, it leads with the forecast's gist and stops; turns the
// edit has nothing to say about still answer as recorded, and Alice's album
// still cannot be searched for. Offered media_search back, it leads with
// the count and offers the first.
//
// verifies SPEC §9.2
func TestModelAnswersBrieflyUnderAnEditedPrompt(t *testing.T) {
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := household.NewModel(store)
	edited := ollama.DefaultPrompt + "\n\nWhen there are several results, say how many, offer the first, and stop."
	if _, v, _ := m.Engines("qwen3-32b@1", edited, registry.Offered()); v.Prompt != "sys@edited" || v.ToolSchema != "tools@8" {
		t.Errorf("an edited prompt runs as %+v, want sys@edited under the registry's tools@8", v)
	}
	weather := run(t, m, edited, household.ConvWeather, "what's the weather today")
	if weather.Said() != "Rain from three, high of fourteen. Take an umbrella." || len(weather.Calls) != 1 || weather.Calls[0].Tool != "ha_get_state" {
		t.Errorf("the weather under the edit = %+v", weather)
	}
	list := run(t, m, edited, household.ConvList, "add oat milk to the shopping list")
	if list.Said() != "Added oat milk." || list.Calls[0].Tool != "ha_call_service" {
		t.Errorf("the shopping list changed under an edit about results: %+v", list)
	}
	if zep := run(t, m, edited, household.ConvZeppelin, "play something by zeppelin"); len(zep.Calls) != 0 || !strings.Contains(zep.Said(), "I can't search for music yet") {
		t.Errorf("zeppelin under the edit, offered no search = %+v", zep)
	}

	eng, _, err := m.Engines("qwen3-32b@1", edited, registry.Specs)
	if err != nil {
		t.Fatal(err)
	}
	zep, err := rerun.Run(context.Background(), eng, household.ConvZeppelin, rerun.Turn{Text: "play something by zeppelin"})
	if err != nil {
		t.Fatal(err)
	}
	if zep.Said() != "I found three albums. Want Led Zeppelin one?" || len(zep.Calls) != 1 || zep.Calls[0].Tool != "media_search" {
		t.Errorf("zeppelin under the edit, offered media_search = %+v", zep)
	}
}

// Offered a reworded ha_get_state, the model checks the garage's contact
// sensor as it does when told to be brief. Offered media_search back, which
// chorusd does not offer, the schema is the reviewer's edit too. Offered no
// ha_call_service, it never calls what it was not offered.
//
// verifies SPEC §9.2
func TestModelAnswersAnEditedToolSchemaWithWhatItIsOffered(t *testing.T) {
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m := household.NewModel(store)
	reworded := registry.Offered()
	getState := reworded["ha_get_state"]
	getState.ModelDescription = "Read one entity's current state. For a door or cover, read its contact sensor."
	reworded["ha_get_state"] = getState
	eng, v, err := m.Engines("qwen3-32b@1", ollama.DefaultPrompt, reworded)
	if err != nil {
		t.Fatal(err)
	}
	if v.Prompt != household.Versions().Prompt || v.ToolSchema != "tools@edited" {
		t.Errorf("a reworded tool runs as %+v, want the day's prompt and tools@edited", v)
	}
	garage, err := rerun.Run(context.Background(), eng, household.ConvGarage, rerun.Turn{Text: "is the garage door closed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(garage.Calls) != 1 || !strings.Contains(garage.Calls[0].Args, "binary_sensor.garage_door_contact") {
		t.Errorf("the garage under the reworded tool = %+v", garage)
	}
	if _, v, _ := m.Engines("qwen3-32b@1", ollama.DefaultPrompt, registry.Specs); v.ToolSchema != "tools@edited" {
		t.Errorf("media_search offered back runs as %q, want tools@edited", v.ToolSchema)
	}

	noService := registry.Offered()
	delete(noService, "ha_call_service")
	eng, _, _ = m.Engines("qwen3-32b@1", ollama.DefaultPrompt, noService)
	list, err := rerun.Run(context.Background(), eng, household.ConvList, rerun.Turn{Text: "add oat milk to the shopping list"})
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Calls) != 0 || list.Said() == "" {
		t.Errorf("the shopping list offered no ha_call_service = %+v, want speech and no call", list)
	}
}

// A turn the household never said is refused, not invented.
func TestModelRefusesATurnItNeverHeard(t *testing.T) {
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	eng, _, _ := household.NewModel(store).Engines("qwen3-32b@1", ollama.DefaultPrompt, registry.Offered())
	_, err = eng.Turn(context.Background(), sess.Input{ConversationID: household.ConvGarage, Speaker: "teagan", Text: "open the garage"})
	if err == nil || !strings.Contains(err.Error(), `never heard "open the garage"`) {
		t.Errorf("err = %v, want a refusal naming the turn", err)
	}
}
