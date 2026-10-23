//go:build db

// Tagged db because `task test` is hermetic: no network, no docker, no
// Postgres. Run with `task test:db`.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
)

// Teagan asks the kitchen whether it will rain in Porto on Saturday. The
// weather server's forecast starts a long answer, and she cuts it off: "just
// tell me if I need an umbrella". chorusd recorded what it offered at
// startup, so the harvested candidate ships with the forecast among its
// tools rather than context_missing.
//
// verifies SPEC §6, §9.1, §13
func TestAHarvestedMCPTurnCarriesTheToolsItWasOffered(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, journal.DSN())
	if err == nil {
		err = pool.Ping(ctx)
	}
	if err != nil {
		if os.Getenv(journal.DSNEnv) != "" {
			t.Fatalf("%s is set but unreachable: %v", journal.DSNEnv, err)
		}
		t.Skipf("no Postgres at the default DSN (%v); run `docker compose up -d postgres`", err)
	}
	t.Cleanup(pool.Close)
	for _, m := range []func(context.Context, journal.Querier) error{journal.Migrate, contexts.Migrate} {
		if err := m(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}

	forecast, err := registry.Provide(registry.ToolSpec{
		Name: "mcp_weather_get_forecast", Description: "Get the weather forecast for a city.",
		Params:      []registry.ParamSpec{{Name: "city", Type: "string", Description: "City name.", Required: true}},
		OnInterrupt: registry.InterruptCancel, Scope: registry.ScopeHousehold, Timeout: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := registry.Offered()
	tools[forecast.Name] = forecast
	if err := contexts.NewPgStore(pool).Record(ctx, contexts.Offer{
		Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt, ToolSchema: ollama.ToolSchema(tools),
	}); err != nil {
		t.Fatal(err)
	}

	conv := "conv-porto-" + time.Now().UTC().Format("150405.000000")
	v := journal.Versions{Model: "qwen3:32b", Prompt: ollama.PromptVersion(ollama.DefaultPrompt), ToolSchema: ollama.ToolSchemaVersion(tools)}
	j := journal.New(journal.NewPgStore(pool), journal.FixedClock(time.Unix(1_760_036_400, 0)), v)
	rec := func(kind journal.Kind, audio string, fields ...string) journal.Record {
		r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
		for i := 0; i+1 < len(fields); i += 2 {
			r.Fields[fields[i]] = fields[i+1]
		}
		return r
	}
	for _, r := range []journal.Record{
		rec(journal.KindSessionOpened, "", "satellite", "kitchen", "speaker_id", "teagan", "resumed", "false"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/porto-1", "text", "will it rain in porto on saturday", "speaker_id", "teagan"),
		rec(journal.KindToolCalled, "", "tool", forecast.Name, "call_id", "call_w1", "args_json", `{"city":"Porto"}`),
		rec(journal.KindToolResult, "", "call_id", "call_w1", "outcome", "ok", "result_json", `{"text":"Saturday: rain from 12:00, 17°C, wind 20 km/h."}`),
		rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s1", "args_json", `{"mode":"queue","streamed":true}`),
		rec(journal.KindBargeInDetected, "blob://mic/porto-2", "tts_position_ms", "1900"),
		rec(journal.KindSpeechTruncated, "blob://tts/porto-s1", "spoken_text", "Saturday in Porto starts dry, then", "unspoken_text", " rain from midday with a high of seventeen and a wind from the north.", "frames_played", "45600"),
		rec(journal.KindToolResult, "", "call_id", "s1", "outcome", "cancelled"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
		rec(journal.KindUtteranceTranscribed, "blob://mic/porto-3", "text", "just tell me if i need an umbrella", "speaker_id", "teagan"),
		rec(journal.KindToolCalled, "", "tool", "speak", "call_id", "s2", "args_json", `{"mode":"queue","streamed":true}`),
		rec(journal.KindSpeechSpoken, "blob://tts/porto-s2", "text", "Yes, from midday.", "frames_played", "24000"),
		rec(journal.KindToolResult, "", "call_id", "s2", "outcome", "ok"),
		rec(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", "stop"),
	} {
		if _, err := j.Append(ctx, conv, r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}

	var out, report bytes.Buffer
	if err := run(ctx, []string{conv}, &out, &report); err != nil {
		t.Fatalf("harvest: %v\n%s", err, report.String())
	}
	line := strings.TrimSpace(out.String())
	if line == "" || strings.Contains(line, "\n") {
		t.Fatalf("harvest wrote %q, want the one candidate", line)
	}
	var row struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
		Meta struct {
			ContextMissing []string `json:"context_missing"`
		} `json:"meta"`
	}
	if err := json.Unmarshal([]byte(line), &row); err != nil {
		t.Fatal(err)
	}
	var offered bool
	for _, tool := range row.Tools {
		offered = offered || tool.Function.Name == forecast.Name
	}
	if !offered || len(row.Meta.ContextMissing) != 0 {
		t.Errorf("the candidate offers the forecast: %v, missing %v; want it offered and nothing missing", offered, row.Meta.ContextMissing)
	}
}
