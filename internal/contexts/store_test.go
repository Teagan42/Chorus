package contexts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/registry"
)

// The household's weather and notes servers, as chorusd offers their tools
// beside the registry's: the forecast for anyone, the shopping list for
// identified people after a yes.
func houseTools(t *testing.T, servers ...string) map[string]registry.ToolSpec {
	t.Helper()
	declared := map[string]registry.ToolSpec{
		"weather": {
			Name: "mcp_weather_get_forecast", Description: "Get the weather forecast for a city.",
			Params: []registry.ParamSpec{
				{Name: "city", Type: "string", Description: "City name.", Required: true},
				{Name: "days", Type: "integer", Description: "Days ahead."},
			},
			OnInterrupt: registry.InterruptCancel, Scope: registry.ScopeHousehold,
			Timeout: 8 * time.Second,
		},
		"notes": {
			Name: "mcp_notes_add_to_shopping_list", Description: "Add an item to the household shopping list.",
			Params:      []registry.ParamSpec{{Name: "item", Type: "string", Description: "What to buy, e.g. oat milk.", Required: true}},
			OnInterrupt: registry.InterruptCancel, Scope: registry.ScopePerson, UnknownSpeaker: "deny",
			Timeout: 5 * time.Second, RequiresConfirmation: true,
		},
	}
	offered := registry.Offered()
	for _, s := range servers {
		spec, err := registry.Provide(declared[s])
		if err != nil {
			t.Fatalf("%s: %v", s, err)
		}
		offered[spec.Name] = spec
	}
	return offered
}

// houseOffer is what chorusd offers with servers connected.
func houseOffer(t *testing.T, servers ...string) Offer {
	return Offer{Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt, ToolSchema: ollama.ToolSchema(houseTools(t, servers...))}
}

// newStore builds an empty store.
type newStore func(t *testing.T) Store

var storeConformance = map[string]func(*testing.T, Store){
	// The labels an offer is kept under are the ones every engine records
	// for it, so a turn's versions find its text.
	"a recorded offer resolves the labels its turns carry": func(t *testing.T, s Store) {
		ctx := context.Background()
		tools := houseTools(t, "weather", "notes")
		o := houseOffer(t, "weather", "notes")
		if err := s.Record(ctx, o); err != nil {
			t.Fatalf("record: %v", err)
		}
		got, err := s.Recorded(ctx)
		if err != nil {
			t.Fatalf("recorded: %v", err)
		}
		engines := map[string][2]string{
			"ollama": {ollama.PromptVersion(ollama.DefaultPrompt), ollama.ToolSchemaVersion(tools)},
		}
		v, err := vllm.New(vllm.Config{BaseURL: "http://vllm.lan:8000", Model: "Qwen/Qwen3-32B", Specs: tools})
		if err != nil {
			t.Fatal(err)
		}
		engines["vllm"] = [2]string{v.Versions().Prompt, v.Versions().ToolSchema}
		for name, labels := range engines {
			if p, ok := got.Prompt(labels[0]); !ok || p != ollama.DefaultPrompt {
				t.Errorf("%s's prompt %s does not resolve to the persona", name, labels[0])
			}
			schema, ok := got.ToolSchema(labels[1])
			if !ok {
				t.Fatalf("%s's tool schema %s does not resolve", name, labels[1])
			}
			parsed, err := ollama.ParseToolSchema(schema)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for _, tool := range []string{"mcp_weather_get_forecast", "mcp_notes_add_to_shopping_list", "ha_call_service"} {
				if _, ok := parsed[tool]; !ok {
					t.Errorf("%s's schema does not declare %s", name, tool)
				}
			}
		}
	},

	// chorusd records at every start; a restart with the same servers adds
	// nothing, and one after the notes server was added keeps both.
	"a restart keeps one row per version and a new server adds one": func(t *testing.T, s Store) {
		ctx := context.Background()
		for _, o := range []Offer{houseOffer(t, "weather"), houseOffer(t, "weather"), houseOffer(t, "weather", "notes")} {
			if err := s.Record(ctx, o); err != nil {
				t.Fatalf("record: %v", err)
			}
		}
		got, err := s.Recorded(ctx)
		if err != nil {
			t.Fatalf("recorded: %v", err)
		}
		if len(got.Prompts) != 1 || len(got.ToolSchemas) != 2 {
			t.Errorf("kept %d prompts and %d schemas, want 1 and 2", len(got.Prompts), len(got.ToolSchemas))
		}
	},

	"an offer with no prompt or a schema that is not JSON is refused whole": func(t *testing.T, s Store) {
		ctx := context.Background()
		for name, o := range map[string]Offer{
			"no prompt":   {Summary: ollama.SummaryPrompt, ToolSchema: houseOffer(t, "weather").ToolSchema},
			"not JSON":    {Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt, ToolSchema: `[{"name": "mcp_weather_get_forecast"`},
			"blank tools": {Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt},
		} {
			if err := s.Record(ctx, o); err == nil {
				t.Errorf("%s: recorded", name)
			}
		}
		got, err := s.Recorded(ctx)
		if err != nil {
			t.Fatalf("recorded: %v", err)
		}
		if len(got.Prompts)+len(got.ToolSchemas) != 0 {
			t.Errorf("a refused offer left %d prompts and %d schemas", len(got.Prompts), len(got.ToolSchemas))
		}
	},
}

func runStoreConformance(t *testing.T, fresh newStore) {
	for name, check := range storeConformance {
		t.Run(name, func(t *testing.T) { check(t, fresh(t)) })
	}
}

// verifies SPEC §8, §13
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) Store { return NewMemStore() })
}

// A recorded label is text the build cannot name: the house's MCP tools.
// Chained after the build and the archive, it is what resolves it.
//
// verifies SPEC §6, §13
func TestAnMCPVersionResolvesOnlyFromWhatWasRecorded(t *testing.T) {
	ctx := context.Background()
	o := houseOffer(t, "weather")
	_, label, err := o.Versions()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := Default().ToolSchema(label); ok {
		t.Fatal("the build or the archive already holds a schema with the weather server's tools")
	}
	s := NewMemStore()
	if err := s.Record(ctx, o); err != nil {
		t.Fatal(err)
	}
	recorded, err := s.Recorded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Chain{Default(), recorded}.ToolSchema(label)
	if !ok || !strings.Contains(got, "mcp_weather_get_forecast") {
		t.Errorf("the chain resolved %s to %q", label, got)
	}
}
