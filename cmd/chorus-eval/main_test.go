package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

const seed = "../../eval/household.yaml"

// testEnv reads the household's day where the journal would be, and the
// judgements given, with getenv answering from vars.
func testEnv(t *testing.T, decisions curation.Store, vars map[string]string) (env, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errs bytes.Buffer
	return env{
		stdout: &out, stderr: &errs, getenv: func(k string) string { return vars[k] },
		open: func(ctx context.Context) (stores, error) {
			store, err := household.Journal(ctx)
			return stores{journal: store, decisions: decisions, kept: contexts.NewMemStore(), close: func() {}}, err
		},
		clock: journal.FixedClock(household.ReviewedAt()),
	}, &out, &errs
}

func write(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// noTimerCancel is a candidate tool schema that lost timer_cancel.
func noTimerCancel(t *testing.T) string {
	t.Helper()
	tools := registry.Offered()
	delete(tools, "timer_cancel")
	return write(t, "tools.json", ollama.ToolSchema(tools))
}

// A candidate tool set without timer_cancel cannot call off the pasta
// timer: the case regresses, and the gate exits 1.
//
// verifies SPEC §13
func TestARegressionExitsOne(t *testing.T) {
	e, out, errs := testEnv(t, nil, nil)
	if code := run(context.Background(), []string{"-demo", "-cache", t.TempDir(), "-tools", noTimerCancel(t), seed}, e); code != exitRegressed {
		t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitRegressed, out, errs)
	}
	if !strings.Contains(out.String(), "pasta-cancel-by-id") || !strings.Contains(out.String(), "REGRESSED: 1 check regressed") {
		t.Errorf("stdout:\n%s", out)
	}
}

// Today's build holds its ground on the seed suite and exits 0, as
// `task eval:demo` runs it; so does the build offered the day's tools from
// a file. Flags may follow the suite, as `task eval -- <suite> …` passes them.
//
// verifies SPEC §13
func TestACandidateThatHoldsItsGroundExitsZero(t *testing.T) {
	for _, args := range [][]string{
		{"-demo", "-no-cache", seed},
		{"-demo", seed, "-no-cache", "-tools", write(t, "tools.json", ollama.ToolSchema(registry.Specs))},
	} {
		e, out, errs := testEnv(t, nil, nil)
		if code := run(context.Background(), args, e); code != exitOK {
			t.Fatalf("%v: exit %d, want 0\nstdout:\n%s\nstderr:\n%s", args, code, out, errs)
		}
		if !strings.Contains(out.String(), "ok: 25 checks compared") {
			t.Errorf("%v: stdout:\n%s", args, out)
		}
	}
}

// A setup that cannot run exits 2 with the reason, never 1: a broken suite,
// an unknown provider, no model, an unreadable prompt file.
//
// verifies SPEC §13
func TestASetupThatCannotRunExitsTwoWithTheReason(t *testing.T) {
	bad := write(t, "bad.yaml", "name: x\ncases: [{id: a, ref: conv-0705-kitchen, expect: {says: {max_words: 3}}}]")
	gone := write(t, "gone.yaml", "name: x\ncases: [{id: a, ref: conv-0101-attic/2, expect: {says: {max_words: 3}}}]")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"bad ref", []string{"-demo", bad}, "<conversation>/<seq>"},
		{"ref the log lacks", []string{"-demo", gone}, "conv-0101-attic has no log"},
		{"unknown provider", []string{"-demo", "-provider", "llamafile", seed}, `-provider "llamafile"`},
		{"no model", []string{seed}, "no model to ask: set OLLAMA_URL"},
		{"no prompt file", []string{"-demo", "-prompt", "/nonexistent/persona.txt", seed}, "-prompt"},
		{"bad tools", []string{"-demo", "-tools", write(t, "tools.json", `[{"type":"function"}]`), seed}, "-tools"},
		{"bad baseline", []string{"-demo", "-baseline", "yesterday", seed}, "-baseline"},
		{"too many repetitions", []string{"-demo", "-reps", "1000", seed}, "-reps 1000"},
		{"no suite", []string{"-demo"}, "usage"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, out, errs := testEnv(t, nil, nil)
			if code := run(context.Background(), tc.args, e); code != exitError {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
			}
			if !strings.Contains(errs.String(), tc.want) {
				t.Errorf("stderr = %s, want it to say %s", errs, tc.want)
			}
		})
	}
}

// The JSON and Markdown reports land where asked, and a second scoring with
// the same cache asks the model nothing.
//
// verifies SPEC §13
func TestTheReportsLandWhereAskedAndASecondRunIsCached(t *testing.T) {
	dir, cache, tools := t.TempDir(), t.TempDir(), noTimerCancel(t)
	js, md := filepath.Join(dir, "eval.json"), filepath.Join(dir, "eval.md")
	for i := range 2 {
		e, out, errs := testEnv(t, nil, nil)
		if code := run(context.Background(), []string{"-demo", "-cache", cache, "-tools", tools, "-json", js, "-markdown", md, "-reps", "2", seed}, e); code != exitRegressed {
			t.Fatalf("run %d: exit %d\nstdout:\n%s\nstderr:\n%s", i, code, out, errs)
		}
	}
	b, err := os.ReadFile(js)
	if err != nil {
		t.Fatal(err)
	}
	var rep evalsuite.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Suite != "household-thursday" || !rep.Regressed || rep.Candidate.Runs != 2 {
		t.Errorf("json report = %+v", rep)
	}
	for _, c := range rep.Cases {
		for _, run := range append(c.Baseline, c.Candidate...) {
			if !run.Cached {
				t.Errorf("%s seed %d was asked again on the second run", c.ID, run.Seed)
			}
		}
	}
	m, err := os.ReadFile(md)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(m), "## Eval: household-thursday") || !strings.Contains(string(m), "| `pasta-cancel-by-id` |") {
		t.Errorf("markdown report:\n%s", m)
	}
}

// The recorded baseline asks nobody: only the candidate is run, and today's
// build holds its ground against what the day said.
//
// verifies SPEC §13
func TestTheRecordedBaselineScoresWhatTheDaySaid(t *testing.T) {
	e, out, errs := testEnv(t, nil, nil)
	code := run(context.Background(), []string{"-demo", "-no-cache", "-baseline", "recorded", "-json", "-", seed}, e)
	if code != exitOK {
		t.Fatalf("exit %d\nstderr:\n%s", code, errs)
	}
	if !strings.Contains(out.String(), "baseline: the recorded takes") {
		t.Errorf("stdout:\n%s", out)
	}
}

// draft writes a suite of what reviewers judged, which parses as it is.
//
// verifies SPEC §9.2, §13
func TestDraftWritesASuiteOfWhatReviewersJudged(t *testing.T) {
	decisions := curation.NewMemStore()
	if err := decisions.PutAnnotation(context.Background(), curation.Annotation{
		ConversationID: household.ConvList, Seq: 2, Labels: []curation.Label{curation.LabelExemplar}, AnnotatedAt: household.ReviewedAt(),
	}); err != nil {
		t.Fatal(err)
	}
	e, out, errs := testEnv(t, decisions, nil)
	if code := run(context.Background(), []string{"draft", "-name", "thursday"}, e); code != exitOK {
		t.Fatalf("exit %d\nstderr:\n%s", code, errs)
	}
	s, err := evalsuite.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("draft does not parse: %v\n%s", err, out)
	}
	if s.Name != "thursday" || len(s.Cases) != 1 || s.Cases[0].Ref != household.ConvList+"/2" {
		t.Errorf("draft = %+v", s)
	}

	e, _, errs = testEnv(t, curation.NewMemStore(), nil)
	if code := run(context.Background(), []string{"draft"}, e); code != exitError || !strings.Contains(errs.String(), "nothing judged yet") {
		t.Errorf("an empty review drafted: exit %d, %s", code, errs)
	}
}

// -print-prompt and -print-tools write what a candidate file starts from.
//
// verifies SPEC §13
func TestThePromptAndToolsToStartFromArePrinted(t *testing.T) {
	e, out, _ := testEnv(t, nil, nil)
	if code := run(context.Background(), []string{"-print-prompt"}, e); code != exitOK || out.String() != ollama.DefaultPrompt {
		t.Errorf("exit %d, printed a prompt that is not the build's", code)
	}
	e, out, _ = testEnv(t, nil, nil)
	if code := run(context.Background(), []string{"-print-tools"}, e); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if _, err := ollama.ParseToolSchema(out.String()); err != nil {
		t.Errorf("printed tools do not read back: %v", err)
	}
}

// A household on vLLM scores the engine that answers it: the provider and
// model follow CHORUS_LLM_PROVIDER as chorusd reads them, and a vLLM setup
// that cannot run exits 2 naming what is missing (ADR-0077).
//
// verifies SPEC §13
func TestTheProviderIsTheOneChorusdAnswersWith(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars map[string]string
		args []string
		want string
	}{
		{"vllm without a server", map[string]string{llmProviderEnv: "vllm", vllmModelEnv: "Qwen/Qwen3-32B-AWQ"}, []string{seed}, "no model to ask: set VLLM_URL"},
		{"vllm named on the command line", nil, []string{"-provider", "vllm", seed}, "no model to ask: set VLLM_URL"},
		{"thinking that is not a boolean", map[string]string{llmProviderEnv: "vllm", vllmURLEnv: "http://127.0.0.1:1", vllmThinkingEnv: "maybe"}, []string{seed}, `VLLM_THINKING="maybe"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, out, errs := testEnv(t, nil, tc.vars)
			if code := run(context.Background(), tc.args, e); code != exitError {
				t.Fatalf("exit %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitError, out, errs)
			}
			if !strings.Contains(errs.String(), tc.want) {
				t.Errorf("stderr = %s, want it to say %s", errs, tc.want)
			}
		})
	}

	e, _, _ := testEnv(t, nil, map[string]string{llmProviderEnv: "vllm", vllmModelEnv: "Qwen/Qwen3-32B-AWQ", ollamaModelEnv: "qwen3:32b"})
	o, code, ok := parseFlags([]string{seed}, e)
	if !ok || code != 0 {
		t.Fatalf("parse: code %d", code)
	}
	if o.provider != "vllm" || o.model != "Qwen/Qwen3-32B-AWQ" {
		t.Errorf("provider %q model %q, want vllm and the model vLLM serves", o.provider, o.model)
	}
	if _, err := vllmEngines(func(k string) string {
		return map[string]string{vllmURLEnv: "http://127.0.0.1:1", vllmThinkingEnv: "true"}[k]
	}, nil); err != nil {
		t.Errorf("a configured vLLM did not build: %v", err)
	}
}

// portoTools is what chorusd offered with the household's weather server
// connected: the registry's tools and the server's forecast.
func portoTools(t *testing.T) map[string]registry.ToolSpec {
	t.Helper()
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
	return tools
}

// portoJournal is Teagan asking the kitchen whether it will rain in Porto
// on Saturday, answered from the weather server.
func portoJournal(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3:32b", Prompt: ollama.PromptVersion(ollama.DefaultPrompt), ToolSchema: ollama.ToolSchemaVersion(portoTools(t))}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_036_400, 0)), v)
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/porto-1", Fields: map[string]string{"text": "will it rain in porto on saturday", "speaker_id": "teagan"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "mcp_weather_get_forecast", "call_id": "call_w1", "args_json": `{"city":"Porto"}`}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "call_w1", "outcome": "ok", "result_json": `{"text":"Saturday: rain from 12:00, 17°C."}`}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"mode":"queue","streamed":true}`}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/porto-s1", Fields: map[string]string{"text": "Rain from midday in Porto on Saturday, high of seventeen.", "frames_played": "48000"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s1", "outcome": "ok"}},
		{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}},
	} {
		if _, err := j.Append(context.Background(), "conv-porto", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// A case names Teagan's Porto turn. Read from the journal's database with
// what chorusd recorded, it is asked and holds its ground; with nothing
// recorded, as before chorusd kept its offers, it is skipped and says why.
//
// verifies SPEC §6, §13
func TestAnMCPTurnIsAskedFromWhatChorusdRecorded(t *testing.T) {
	suite := write(t, "porto.yaml", `name: porto
cases:
  - id: porto-rain
    ref: conv-porto/2
    expect:
      tool_called: [{tool: mcp_weather_get_forecast, args: {city: Porto}}]
      says: {contains: [rain]}
`)
	for _, recorded := range []bool{true, false} {
		kept := contexts.NewMemStore()
		if recorded {
			o := contexts.Offer{Prompt: ollama.DefaultPrompt, Summary: ollama.SummaryPrompt, ToolSchema: ollama.ToolSchema(portoTools(t))}
			if err := kept.Record(context.Background(), o); err != nil {
				t.Fatal(err)
			}
		}
		e, out, errs := testEnv(t, nil, nil)
		store := portoJournal(t)
		e.open = func(context.Context) (stores, error) {
			return stores{journal: store, decisions: curation.NewMemStore(), kept: kept, close: func() {}}, nil
		}
		// A suite whose only case is skipped judges nothing, which fails.
		want := exitRegressed
		if recorded {
			want = exitOK
		}
		if code := run(context.Background(), []string{"-provider", "demo", "-no-cache", "-json", "-", suite}, e); code != want {
			t.Fatalf("recorded %v: exit %d, want %d\nstdout:\n%s\nstderr:\n%s", recorded, code, want, out, errs)
		}
		var rep struct {
			Skipped int `json:"skipped"`
		}
		if err := json.NewDecoder(strings.NewReader(out.String()[strings.Index(out.String(), "{"):])).Decode(&rep); err != nil {
			t.Fatalf("recorded %v: %v\n%s", recorded, err, out)
		}
		if recorded && rep.Skipped != 0 {
			t.Errorf("with chorusd's record the case was skipped:\n%s", out)
		}
		if !recorded && (rep.Skipped != 1 || !strings.Contains(out.String(), "context_missing: tool_schema")) {
			t.Errorf("without it the case was not skipped as context_missing:\n%s", out)
		}
	}
}
