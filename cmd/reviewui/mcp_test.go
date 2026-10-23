package main

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	sess "github.com/teagan42/chorus/internal/session"
)

const (
	convPorto     = "conv-porto"
	portoForecast = "mcp_weather_get_forecast"
	askPorto      = "will it rain in porto on saturday"
	saidPorto     = "Rain from midday in Porto on Saturday, high of seventeen."
)

// portoTools is what chorusd offered with the household's weather server
// connected: the registry's tools and the server's forecast, under the
// household's relaxed policy for it (mcp.example.yaml).
func portoTools(t *testing.T) map[string]registry.ToolSpec {
	t.Helper()
	forecast, err := registry.Provide(registry.ToolSpec{
		Name: portoForecast, Description: "Get the weather forecast for a city.",
		Params: []registry.ParamSpec{
			{Name: "city", Type: "string", Description: "City name.", Required: true},
			{Name: "days", Type: "integer", Description: "Days ahead."},
		},
		OnInterrupt: registry.InterruptCancel, Scope: registry.ScopeHousehold, Timeout: 8 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := registry.Offered()
	tools[portoForecast] = forecast
	return tools
}

// portoOffer is what that chorusd recorded at startup (ADR-0101).
func portoOffer(t *testing.T) contexts.Offer {
	return contexts.Offer{Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt, ToolSchema: ollama.ToolSchema(portoTools(t))}
}

// portoLog is Teagan in the kitchen on Thursday evening, asking whether it
// will rain in Porto, where she flies on Saturday. Home Assistant knows only
// the house's weather; the forecast comes from the weather server, and the
// turn's events carry the labels a real engine offered it records.
func portoLog(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{
		Model: "qwen3:32b", Prompt: ollama.PromptVersion(ollama.DefaultPrompt),
		ToolSchema: ollama.ToolSchemaVersion(portoTools(t)), STT: "parakeet-tdt-0.6b-v3", TTS: "kokoro-82m:af_heart",
	}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_036_400, 0)), v)
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/1", Fields: map[string]string{"text": askPorto, "speaker_id": "teagan"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": portoForecast, "call_id": "call_w1", "args_json": `{"city":"Porto","days":2}`}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "call_w1", "outcome": "ok", "result_json": `{"text":"Saturday: rain from 12:00, 17°C."}`}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"mode":"queue","streamed":true}`}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/s1", Fields: map[string]string{"text": saidPorto, "frames_played": "48000"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s1", "outcome": "ok"}},
		{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}},
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended", "satellite": "kitchen"}},
	} {
		if _, err := j.Append(context.Background(), convPorto, r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// newPortoServer serves Teagan's Porto forecast, with what chorusd recorded
// when recorded is set, as the review box reads it from the journal's
// database.
func newPortoServer(t *testing.T, recorded bool, h *scripted) (*server, curation.Store) {
	t.Helper()
	decisions := curation.NewMemStore()
	s := newServer(portoLog(t), decisions, fixtureBlobs(t), func() time.Time { return time.Unix(1_760_040_000, 0).UTC() })
	if recorded {
		kept := contexts.NewMemStore()
		if err := kept.Record(context.Background(), portoOffer(t)); err != nil {
			t.Fatal(err)
		}
		s.kept = kept
	}
	if h != nil {
		// Labelled as a real engine labels what it is offered, so the page
		// can tell the recorded schema from an edited one.
		s.engineFor = func(model, prompt string, tools map[string]registry.ToolSpec) (sess.Engine, journal.Versions, error) {
			e, v, err := h.engineFor(model, prompt, tools)
			v.Prompt, v.ToolSchema = ollama.PromptVersion(prompt), ollama.ToolSchemaVersion(tools)
			return e, v, err
		}
	}
	return s, decisions
}

// porto answers Teagan's question the way the recorded turn did.
func porto() *scripted {
	return &scripted{answers: map[string][]sess.Action{
		askPorto: {
			sess.ToolCall{ID: "call_w1", Tool: portoForecast, Args: `{"city":"Porto","days":2}`},
			sess.SpeechDelta{CallID: "s1", Text: saidPorto, Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
	}}
}

// Teagan's Porto answer is labelled an exemplar. Its SFT row ships with
// the tools the turn was offered, the forecast among them, read from what
// chorusd recorded. Without that record, the row says so instead of
// guessing, as every MCP turn's did before.
//
// verifies SPEC §6, §9.1, §13
func TestAnMCPTurnShipsWithTheToolsItWasOffered(t *testing.T) {
	for _, recorded := range []bool{true, false} {
		s, decisions := newPortoServer(t, recorded, nil)
		if err := decisions.PutAnnotation(context.Background(), curation.Annotation{
			ConversationID: convPorto, Seq: 2, Labels: []curation.Label{curation.LabelExemplar}, AnnotatedAt: time.Unix(1_760_039_000, 0).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
		sft := strings.TrimSpace(get(t, s, "/export/sft.jsonl"))
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
		if err := json.Unmarshal([]byte(sft), &row); err != nil {
			t.Fatalf("recorded %v: the download is %q: %v", recorded, sft, err)
		}
		var names []string
		for _, tool := range row.Tools {
			names = append(names, tool.Function.Name)
		}
		offered := strings.Contains(strings.Join(names, " "), portoForecast)
		if recorded && (!offered || len(row.Meta.ContextMissing) != 0) {
			t.Errorf("with chorusd's record the row offers %v, missing %v; want the forecast and nothing missing", names, row.Meta.ContextMissing)
		}
		if !recorded && (len(row.Tools) != 0 || strings.Join(row.Meta.ContextMissing, ",") != "tool_schema") {
			t.Errorf("without it the row offers %v, missing %v; want no tools and tool_schema missing", names, row.Meta.ContextMissing)
		}
	}
}

// A reviewer re-runs the Porto turn without touching the form. The model is
// offered the forecast the turn had, beside the registry's tools, so the
// same answer is the same: no tool diff, and the call matches.
//
// verifies SPEC §6, §9.2
func TestAReRunOfAnMCPTurnIsOfferedTheHousesTools(t *testing.T) {
	h := porto()
	s, _ := newPortoServer(t, true, h)
	page := get(t, s, replayHref(convPorto))
	missing(t, "Replay", page,
		`&#34;name&#34;: &#34;`+portoForecast+`&#34;`,
		"the registry&#39;s schema with the 1 MCP tool the turns were offered",
		"matches the recorded tool schema",
	)

	res := runReplay(t, s, convPorto, url.Values{"model": {"qwen3:32b"}, "prompt": {ollama.DefaultPrompt}})
	if _, ok := h.offered[len(h.offered)-1][portoForecast]; !ok {
		t.Error("the re-run's model was not offered the weather server's forecast")
	}
	missing(t, "the re-run", res, `tone-muted">same<`, "0 of 1 turns changed")
	if strings.Contains(res, "Tool schema diff") {
		t.Error("an unedited re-run shows a tool schema diff")
	}
}

// Before chorusd recorded what it offered, nothing here holds the Porto
// turn's schema. The re-run starts from the registry's tools, and the page
// says which label it could not read and that a server's tool is not in it.
//
// verifies SPEC §9.2
func TestAReRunSaysWhenTheTurnsToolsAreNotHeld(t *testing.T) {
	h := porto()
	s, _ := newPortoServer(t, false, h)
	label := ollama.ToolSchemaVersion(portoTools(t))
	page := get(t, s, replayHref(convPorto))
	missing(t, "Replay", page, "nothing here holds the text of "+label, "is not in this schema unless pasted in")
	if strings.Contains(page, portoForecast+`&#34;,`) {
		t.Error("the form offers a forecast nothing recorded")
	}
	runReplay(t, s, convPorto, url.Values{"model": {"qwen3:32b"}, "prompt": {ollama.DefaultPrompt}})
	if _, ok := h.offered[len(h.offered)-1][portoForecast]; ok {
		t.Error("the re-run offered a tool nothing recorded")
	}
}
