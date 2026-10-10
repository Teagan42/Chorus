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
	"encoding/json"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
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
	return ask(t, e, session.Input{ConversationID: "models-test", Speaker: "alan", Text: text})
}

// ask runs one ask and reports what the model did, in order.
func ask(t *testing.T, e *ollama.Engine, in session.Input) []session.Action {
	t.Helper()
	text := in.Text
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()

	start := time.Now()
	ch, err := e.Turn(ctx, in)
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
			// What the model wrote is the only clue when it spoke nothing:
			// content the session drops, or reasoning that never finished.
			var c struct{ Content, Thinking string }
			_ = json.Unmarshal([]byte(v.Completion), &c)
			out = append(out, fmt.Sprintf("end:%s content=%q thinking=%q", v.FinishReason, truncate(c.Content), tail(c.Thinking)))
		}
	}
	return out
}

// tail is the end of the model's reasoning, where it decided what to do.
func tail(s string) string {
	if len(s) > 300 {
		return "..." + s[len(s)-300:]
	}
	return s
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

// A slow tool carries what to say while it works as a required argument,
// because a speak call volunteered beside it came and went from run to run.
// Alan asks for a film, which is a library search: the call has to say
// something for the session to speak while it runs.
//
// verifies SPEC §4.1, §11
func TestARealModelSaysSomethingWhileASlowToolWorks(t *testing.T) {
	acts := turn(t, engine(t), "Recommend a movie like Indiana Jones starring Tom Holland.")

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
		Query           string `json:"query"`
		Acknowledgement string `json:"acknowledgement"`
	}
	if err := json.Unmarshal([]byte(search.Args), &args); err != nil {
		t.Fatalf("%s wrote arguments %q: %v", *model, search.Args, err)
	}
	if strings.TrimSpace(args.Acknowledgement) == "" {
		t.Errorf("%s searched with nothing to say while it works: %s", *model, search.Args)
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

// The follow-up ask is only worth making if a real model answers from the
// result it is given rather than calling the tool again or saying nothing.
// Teagan's garage question, after the model said it was checking and read
// the cover: the door is open, so the answer has to say so.
//
// verifies SPEC §4.1, §4.4
func TestARealModelAnswersFromTheResultItIsGiven(t *testing.T) {
	acts := ask(t, engine(t), session.Input{
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

// The front door was held for Teagan's yes. A real model has to ask her
// rather than call straight back with the nonce, and once she says yes it has
// to call again with the same door and the nonce it was handed (SPEC §6).
//
// verifies SPEC §6
func TestARealModelAsksBeforeItUnlocksTheFrontDoor(t *testing.T) {
	const (
		unlock = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
		nonce  = "cf_4c1e9a07"
	)
	held := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "unlock the front door"},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_call_service", Args: unlock},
		{
			Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_call_service", Outcome: "confirmation_required",
			Result: `{"confirmation_required":true,"nonce":"` + nonce + `","note":"Not done. Ask the person; if they say yes, call again with the same arguments and confirmation set to this nonce."}`,
		},
	}
	e := engine(t)
	acts := ask(t, e, session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "unlock the front door", Dialogue: held})
	asked := false
	for _, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			asked = asked || strings.Contains(v.Text, "?")
		case session.ToolCall:
			if v.Tool == "ha_call_service" {
				t.Errorf("%s called back before Teagan answered: %v", *model, describe(acts))
			}
		}
	}
	if !asked {
		t.Errorf("%s did not ask Teagan: %v", *model, describe(acts))
	}

	answered := append(append([]journal.Entry(nil), held...),
		journal.Entry{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Do you want me to unlock the front door?"},
		journal.Entry{Kind: journal.EntryHeard, Text: "yes please"},
	)
	acts = ask(t, e, session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "yes please", Dialogue: answered})
	var call *session.ToolCall
	for _, a := range acts {
		if v, ok := a.(session.ToolCall); ok && v.Tool == "ha_call_service" {
			call = &v
		}
	}
	if call == nil {
		t.Fatalf("%s did not call again on Teagan's yes: %v", *model, describe(acts))
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(call.Args), &args); err != nil {
		t.Fatalf("%s wrote arguments %q: %v", *model, call.Args, err)
	}
	if args["confirmation"] != nonce || args["entity_id"] != "lock.front_door" || args["service"] != "unlock" {
		t.Errorf("%s called with %v, want the front door and confirmation %s", *model, args, nonce)
	}
}
