//go:build models

// Model tier: needs a reachable vLLM OpenAI server, started with the flags
// ADR-0077 names (--enable-auto-tool-choice --tool-call-parser hermes for
// Qwen3). Run with `task test:models`, pointing it at one:
//
//	go test -tags=models ./internal/provider/vllm/ -vllm-url http://host:8000 -vllm-model Qwen/Qwen3-32B-AWQ
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that a real
// model, given this request and this server's tool parser, follows the
// protocol, and that its speech really streams. Decoding is covered
// hermetically against SSE fixtures in decode_test.go.
package vllm_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

var (
	endpoint = flag.String("vllm-url", "", "vLLM server root; skips when empty")
	model    = flag.String("vllm-model", "Qwen/Qwen3-32B-AWQ", "served model name to exercise")
	apiKey   = flag.String("vllm-key", "", "the server's --api-key, if it has one")
	thinking = flag.Bool("vllm-thinking", false, "ask with Qwen3's thinking on")
)

// turnBudget is generous: a server that has just started compiles CUDA
// graphs on its first requests, and these tests are allowed to pay that.
const turnBudget = 3 * time.Minute

func engine(t *testing.T, specs map[string]registry.ToolSpec) *vllm.Engine {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -vllm-url")
	}
	e, err := vllm.New(vllm.Config{BaseURL: *endpoint, Model: *model, APIKey: *apiKey, Thinking: *thinking, Specs: specs})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

// ask runs one ask and reports what the model did, in order.
func ask(t *testing.T, e *vllm.Engine, in session.Input) []session.Action {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()

	start := time.Now()
	ch, err := e.Turn(ctx, in)
	if err != nil {
		t.Fatalf("turn %q: %v", in.Text, err)
	}
	var acts []session.Action
	var first, firstWords time.Duration
	for a := range ch {
		if first == 0 {
			first = time.Since(start)
		}
		if d, ok := a.(session.SpeechDelta); ok && d.Text != "" && firstWords == 0 {
			firstWords = time.Since(start)
		}
		acts = append(acts, a)
	}
	// Logged every run: SPEC §11 budgets ~700 ms to first audio, and a
	// regression here is invisible in a pass/fail.
	t.Logf("%q -> %v (first action %v, first words %v, turn %v)", in.Text, describe(acts),
		first.Round(time.Millisecond), firstWords.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	return acts
}

func turn(t *testing.T, e *vllm.Engine, text string) []session.Action {
	t.Helper()
	return ask(t, e, session.Input{ConversationID: "models-test", Speaker: "alan", Text: text})
}

func describe(acts []session.Action) []string {
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			out = append(out, fmt.Sprintf("speak(%s,last=%v)=%q", v.Mode, v.Last, truncate(v.Text)))
		case session.ToolCall:
			out = append(out, fmt.Sprintf("%s(%s)", v.Tool, truncate(v.Args)))
		case session.TurnEnd:
			out = append(out, fmt.Sprintf("end:%s %s", v.FinishReason, truncate(v.Completion)))
		}
	}
	return out
}

func truncate(s string) string {
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// The model is heard by calling the speak tool, and its words stream: more
// than one delta reaches the session before the call closes, which is what
// vLLM is here for (SPEC §4.1, §10). The completion keeps the arguments as
// written, so the log shows whether the model put mode before its words.
//
// verifies SPEC §4.1
func TestARealModelSpeaksByCallingTheToolAndItsWordsStream(t *testing.T) {
	acts := turn(t, engine(t, nil), "Tell me, in two sentences, why the sky is blue.")

	var deltas int
	var words string
	for _, a := range acts {
		if s, ok := a.(session.SpeechDelta); ok && s.CallID != "" {
			deltas++
			words += s.Text
		}
	}
	if strings.TrimSpace(words) == "" {
		t.Fatalf("%s said nothing through the speak tool: %v", *model, describe(acts))
	}
	if deltas < 2 {
		t.Errorf("%s's speech arrived in %d delta: the arguments did not stream", *model, deltas)
	}
	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok || end.FinishReason == session.FinishError {
		t.Fatalf("the turn did not finish: %v", describe(acts))
	}
	t.Logf("completion: %s", end.Completion)
}

// A slow tool carries what to say while it works as a required argument.
//
// verifies SPEC §4.1, §11
func TestARealModelSaysSomethingWhileASlowToolWorks(t *testing.T) {
	// media_search is deferred, so not offered by default, but it is still
	// the declared slow tool the acknowledgement is measured on.
	acts := turn(t, engine(t, registry.Specs), "Recommend a movie like Indiana Jones starring Tom Holland.")
	var search *session.ToolCall
	for _, a := range acts {
		if v, ok := a.(session.ToolCall); ok && v.Tool == "media_search" {
			search = &v
			break
		}
	}
	if search == nil {
		t.Fatalf("%s never called media_search: %v", *model, describe(acts))
	}
	var args struct {
		Acknowledgement string `json:"acknowledgement"`
	}
	if err := json.Unmarshal([]byte(search.Args), &args); err != nil || strings.TrimSpace(args.Acknowledgement) == "" {
		t.Errorf("%s searched with nothing to say while it works: %s (%v)", *model, search.Args, err)
	}
}

// An out-of-enum mode never reaches the speech channel.
//
// verifies SPEC §4.2
func TestARealModelOnlyEverYieldsADeclaredMode(t *testing.T) {
	for _, a := range turn(t, engine(t, nil), "Tell me a two-sentence story about a cat.") {
		if s, ok := a.(session.SpeechDelta); ok {
			switch s.Mode {
			case session.ModeQueue, session.ModePreempt, session.ModeInterject:
			default:
				t.Errorf("mode %q reached the speech channel", s.Mode)
			}
		}
	}
}

// A model the server does not serve fails loudly rather than streaming
// nothing.
func TestAnUnservedModelFailsTheTurn(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -vllm-url")
	}
	e, err := vllm.New(vllm.Config{BaseURL: *endpoint, Model: "definitely-not-a-model", APIKey: *apiKey})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()
	if _, err := e.Turn(ctx, session.Input{Text: "hello"}); err == nil {
		t.Fatal("an unserved model must fail the turn")
	} else {
		t.Logf("unserved model: %v", err)
	}
}

// Teagan's garage question, after the model said it was checking and read
// the cover: the door is open, so the answer has to say so, from the result
// it is given in OpenAI's shape, paired by call id.
//
// verifies SPEC §4.1, §4.4
func TestARealModelAnswersFromTheResultItIsGiven(t *testing.T) {
	acts := ask(t, engine(t, nil), session.Input{
		ConversationID: "models-test",
		Speaker:        "teagan",
		Text:           "is the garage door closed",
		Dialogue: []journal.Entry{
			{Kind: journal.EntryHeard, Text: "is the garage door closed"},
			{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check."},
			{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
			{
				Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok",
				Result: `{"entity_id":"cover.garage_door","state":"open","attributes":{"friendly_name":"Garage Door","device_class":"garage"}}`,
			},
		},
	})
	var said string
	for _, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			said += v.Text
		case session.ToolCall:
			if v.Tool == "ha_get_state" {
				t.Errorf("%s read the cover again instead of answering: %v", *model, describe(acts))
			}
		}
	}
	if !strings.Contains(strings.ToLower(said), "open") {
		t.Errorf("%s did not say the door is open: %q (%v)", *model, said, describe(acts))
	}
}
