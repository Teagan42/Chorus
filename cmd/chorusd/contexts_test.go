package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
)

// What chorusd records at startup resolves the labels its engine stamps on
// every event, on either engine, the weather server's forecast among the
// tools: no build or archive holds that schema.
//
// verifies SPEC §6, §8, §13
func TestTheOfferIsRecordedUnderTheLabelsItsEventsCarry(t *testing.T) {
	vllmEnv := complete()
	vllmEnv[llmProviderEnv], vllmEnv[vllmURLEnv], vllmEnv[vllmModelEnv] = providerVLLM, "http://vllm:8000", "Qwen/Qwen3-32B"
	for name, env := range map[string]map[string]string{"ollama": complete(), "vllm": vllmEnv} {
		t.Run(name, func(t *testing.T) {
			p, err := buildProviders(configFromEnv(lookup(env)), nil, slog.New(slog.DiscardHandler), staticTools{forecastTool: forecastSpec(t)})
			if err != nil {
				t.Fatalf("providers: %v", err)
			}
			store := contexts.NewMemStore()
			if err := recordOffer(context.Background(), store, p); err != nil {
				t.Fatalf("record: %v", err)
			}
			got, err := store.Recorded(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if prompt, ok := got.Prompt(p.versions.Prompt); !ok || prompt != ollama.DefaultPrompt {
				t.Errorf("prompt %s does not resolve to the persona", p.versions.Prompt)
			}
			schema, ok := got.ToolSchema(p.versions.ToolSchema)
			if !ok || !strings.Contains(schema, forecastTool) {
				t.Errorf("tool schema %s resolves to %q, want the forecast in it", p.versions.ToolSchema, schema)
			}
			if _, ok := contexts.Default().ToolSchema(p.versions.ToolSchema); ok {
				t.Error("the build already names the weather server's schema; this test proves nothing")
			}
		})
	}

	// With no server, the build's own schema is recorded too: a dev build's
	// labels resolve wherever its journal is read.
	p, err := buildProviders(configFromEnv(lookup(complete())), nil, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := contexts.NewMemStore()
	if err := recordOffer(context.Background(), store, p); err != nil {
		t.Fatalf("record without mcp: %v", err)
	}
	if got, _ := store.Recorded(context.Background()); len(got.ToolSchemas) != 1 {
		t.Errorf("recorded %d schemas without mcp, want the build's", len(got.ToolSchemas))
	}
}

// An offer whose text is not what the engine's labels name is refused, not
// filed under a label that never named it.
//
// verifies SPEC §13
func TestAnOfferTheEngineDidNotSendIsNotRecorded(t *testing.T) {
	p, err := buildProviders(configFromEnv(lookup(complete())), nil, slog.New(slog.DiscardHandler), staticTools{forecastTool: forecastSpec(t)})
	if err != nil {
		t.Fatal(err)
	}
	// The engine was offered the forecast; the offer forgot it.
	p.offer.ToolSchema = ollama.ToolSchema(nil)
	store := contexts.NewMemStore()
	if err := recordOffer(context.Background(), store, p); err == nil || !strings.Contains(err.Error(), p.versions.ToolSchema) {
		t.Errorf("error %v, want it to name the engine's label %s", err, p.versions.ToolSchema)
	}
	if got, _ := store.Recorded(context.Background()); len(got.ToolSchemas)+len(got.Prompts) != 0 {
		t.Error("a refused offer was kept")
	}
}

// Alan asks the kitchen for Lisbon's weather, and the weather server
// answers after his yes. Weeks later an eval case names that turn. Read
// from the build and the archive alone, its tool schema is missing, as it
// was before chorusd recorded its offer; with what this process recorded,
// the case resolves, and is asked with the forecast it was offered.
//
// verifies SPEC §6, §8, §13
func TestAlansForecastTurnResolvesFromWhatChorusdRecorded(t *testing.T) {
	w := newWeatherServer(false)
	var offered providers
	r := newRig(t, inventory(), withWeather(t, w, ""), func(d *deps) {
		d.engine = forecastModel()
		offered = d.providers
	})
	recorded := contexts.NewMemStore()
	// What start does once the providers are built.
	if err := recordOffer(context.Background(), recorded, offered); err != nil {
		t.Fatalf("record: %v", err)
	}
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line(askWeather, alan))
	dev.AwaitTTS(t, 2*len(checkFirst))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 2*len(checkFirst)+2*len(sunny))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 2)

	var asked journal.Event
	for _, e := range r.store.ofKind(journal.KindUtteranceTranscribed) {
		if e.Fields["text"] == askWeather {
			asked = e
		}
	}
	if asked.Seq == 0 {
		t.Fatal("Alan's question is not in the log")
	}
	suite, err := evalsuite.Parse([]byte(`name: lisbon
cases:
  - id: lisbon-forecast
    ref: ` + fmt.Sprintf("%s/%d", asked.ConversationID, asked.Seq) + `
    expect:
      tool_called:
        - tool: ` + forecastTool + `
          args: {city: Lisbon}
      asks_confirmation: true
`))
	if err != nil {
		t.Fatalf("suite: %v", err)
	}

	before, err := evalsuite.Resolve(context.Background(), r.store, contexts.Default(), suite)
	if err != nil {
		t.Fatal(err)
	}
	if before[0].Skipped != "context_missing: tool_schema" {
		t.Errorf("from the build alone the case is %q, want context_missing: tool_schema", before[0].Skipped)
	}

	kept, err := recorded.Recorded(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, err := evalsuite.Resolve(context.Background(), r.store, contexts.Chain{contexts.Default(), kept}, suite)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Skipped != "" {
		t.Fatalf("with what chorusd recorded the case is %q", after[0].Skipped)
	}
	if _, ok := after[0].Recorded.Tools[forecastTool]; !ok {
		t.Errorf("the case is asked with %d tools, none of them the forecast", len(after[0].Recorded.Tools))
	}
	if after[0].Recorded.Versions.ToolSchema != offered.versions.ToolSchema {
		t.Errorf("the turn ran under %s, the engine offered %s", after[0].Recorded.Versions.ToolSchema, offered.versions.ToolSchema)
	}
}
