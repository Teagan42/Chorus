package evalfake_test

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/evalsuite/evalfake"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/household"
	"github.com/teagan42/chorus/internal/session"
)

func ask(t *testing.T, tools map[string]registry.ToolSpec, text string) rerun.Take {
	t.Helper()
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m, err := evalfake.New(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	eng, _, err := m.Engine(evalsuite.Config{Model: "qwen3-32b@1", Prompt: ollama.DefaultPrompt, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	take, err := rerun.Ask(context.Background(), eng, session.Input{ConversationID: household.ConvZeppelin, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	return take
}

// Offered the tools it called, the scripted model answers as the day did.
//
// verifies SPEC §13
func TestTheScriptedModelAnswersAsRecordedWhenOfferedWhatItCalled(t *testing.T) {
	take := ask(t, registry.Specs, "play something by zeppelin")
	if len(take.Calls) != 1 || take.Calls[0].Tool != "media_search" {
		t.Errorf("calls = %+v, want the day's media_search", take.Calls)
	}
	if take.Said() != "I found three albums by that artist: Led Zeppelin, Led Zeppelin II and Led Zeppelin IV." {
		t.Errorf("said %q, want the day's answer", take.Said())
	}
}

// Not offered a tool its take called, as media_search is while media is
// deferred (SPEC §14), it calls nothing and says it cannot, rather than
// claim results only that tool would have found.
//
// verifies SPEC §6, §13
func TestTheScriptedModelSaysItCannotWhatItWasNotOffered(t *testing.T) {
	take := ask(t, registry.Offered(), "play something by zeppelin")
	if len(take.Calls) != 0 {
		t.Errorf("calls = %+v, want none: media_search is not offered", take.Calls)
	}
	if take.Said() != evalfake.Cannot {
		t.Errorf("said %q, want %q", take.Said(), evalfake.Cannot)
	}
}
