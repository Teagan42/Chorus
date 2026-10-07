//go:build models

// Model tier: needs a reachable Ollama endpoint. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/provider/ollama/ -ollama-url https://host
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that a real
// model, given this request, follows the protocol. The prompt's clauses are
// measured claims (see DefaultPrompt), and a claim nothing checks is a claim
// that quietly stops being true when a model is swapped. Decoding is covered
// hermetically against captured streams in decode_test.go.
package ollama_test

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/provider/ollama"
	"github.com/teaganglenn/chorus/internal/session"
)

var (
	endpoint = flag.String("ollama-url", "", "Ollama base URL; skips when empty")
	model    = flag.String("ollama-model", "qwen3:14b", "model to exercise")
)

// turnBudget is generous on purpose. A cold load costs ~71 s plus a ~56 s first
// prompt eval, and these tests are allowed to be the request that pays it.
const turnBudget = 3 * time.Minute

func engine(t *testing.T) *ollama.Engine {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -ollama-url")
	}
	e, err := ollama.New(ollama.Config{BaseURL: *endpoint, Model: *model})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

// turn runs one turn and reports what the model did, in order.
func turn(t *testing.T, e *ollama.Engine, text string) []session.Action {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()

	start := time.Now()
	ch, err := e.Turn(ctx, session.Input{ConversationID: "models-test", Speaker: "alan", Text: text})
	if err != nil {
		t.Fatalf("turn %q: %v", text, err)
	}
	var acts []session.Action
	var first time.Duration
	for a := range ch {
		if first == 0 {
			first = time.Since(start)
		}
		acts = append(acts, a)
	}
	// Logged every run: SPEC §11 budgets ~700 ms to first audio, and a
	// regression here is invisible in a pass/fail.
	t.Logf("%q -> %v (first action %v, turn %v)", text, describe(acts), first.Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
	return acts
}

func describe(acts []session.Action) []string {
	out := make([]string, 0, len(acts))
	for _, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			out = append(out, fmt.Sprintf("speak(%s)=%q", v.Mode, truncate(v.Text)))
		case session.ToolCall:
			out = append(out, fmt.Sprintf("%s(%s)", v.Tool, truncate(v.Args)))
		case session.TurnEnd:
			out = append(out, "end:"+v.FinishReason)
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

// The model must speak by calling the tool. Content is dropped by default, so a
// model that answers in content is simply not heard.
//
// verifies SPEC §4.1
func TestARealModelSpeaksByCallingTheTool(t *testing.T) {
	acts := turn(t, engine(t), "Say hello and nothing else.")

	var spoke bool
	for _, a := range acts {
		if s, ok := a.(session.SpeechDelta); ok && strings.TrimSpace(s.Text) != "" {
			spoke = true
		}
	}
	if !spoke {
		t.Errorf("%s said nothing audible: %v", *model, describe(acts))
	}

	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want TurnEnd", acts[len(acts)-1])
	}
	if end.FinishReason == "error" {
		t.Fatalf("the stream failed: %s", end.Completion)
	}
	// The raw completion is the journal's, and replay cannot regenerate it.
	if !strings.Contains(end.Completion, "tool_calls") {
		t.Errorf("no tool call in the completion: %s", truncate(end.Completion))
	}
}

// The reason the slow hint and the sequential framing are in the request at
// all: without them the model calls the slow tool alone and the user waits in
// silence past SPEC §11's budget.
//
// verifies SPEC §4.1, §14
func TestARealModelSpeaksBeforeASlowTool(t *testing.T) {
	acts := turn(t, engine(t), "Find me a lasagne recipe in the media library.")

	speechAt, callAt := -1, -1
	for i, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			if speechAt < 0 && strings.TrimSpace(v.Text) != "" {
				speechAt = i
			}
		case session.ToolCall:
			if callAt < 0 && v.Tool == "media_search" {
				callAt = i
			}
		}
	}
	if callAt < 0 {
		t.Fatalf("%s never called media_search: %v", *model, describe(acts))
	}
	if speechAt < 0 {
		t.Fatalf("%s called a slow tool with no acknowledgement: %v", *model, describe(acts))
	}
	// Order cannot be corrected downstream: calls arrive on separate lines, so
	// fixing it would mean buffering to end of turn.
	if speechAt > callAt {
		t.Errorf("the slow tool was dispatched before the speech covering it: %v", describe(acts))
	}
}

// An out-of-enum mode reaching the speech channel would discard or duck speech
// the user is still hearing, and the endpoint does not validate arguments
// against the schema it was sent.
//
// verifies SPEC §4.2
func TestARealModelOnlyEverYieldsADeclaredMode(t *testing.T) {
	acts := turn(t, engine(t), "Tell me a two-sentence story about a cat.")
	for _, a := range acts {
		s, ok := a.(session.SpeechDelta)
		if !ok {
			continue
		}
		switch s.Mode {
		case session.ModeQueue, session.ModePreempt, session.ModeInterject:
		default:
			t.Errorf("mode %q reached the speech channel", s.Mode)
		}
	}
}

// A model that rejects the request outright has to fail loudly rather than
// stream nothing: several models under 9B answer 400 to the think field, and
// one of them being configured by mistake must not look like silence.
func TestAnUnusableModelFailsTheTurn(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -ollama-url")
	}
	e, err := ollama.New(ollama.Config{BaseURL: *endpoint, Model: "definitely-not-a-model:0b"})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()
	if _, err := e.Turn(ctx, session.Input{Text: "hello"}); err == nil {
		t.Fatal("an unknown model must fail the turn")
	} else {
		t.Logf("unknown model: %v", err)
	}
}
