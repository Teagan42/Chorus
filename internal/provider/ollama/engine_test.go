package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// roundTrip serves a canned response in-process and keeps the request that
// asked for it. No listener: hermetic means hermetic, which is why the repo's
// other transports test over net.Pipe rather than loopback (CONTRIBUTING).
type roundTrip struct {
	status  int
	body    string
	err     error
	reqBody []byte
}

func (rt *roundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		rt.reqBody, _ = io.ReadAll(r.Body)
	}
	if rt.err != nil {
		return nil, rt.err
	}
	status := rt.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(rt.body)),
		Header:     http.Header{},
		Request:    r,
	}, nil
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// engineOn wires an Engine to a canned stream.
func engineOn(t *testing.T, rt *roundTrip, cfg Config) *Engine {
	t.Helper()
	cfg.BaseURL = "http://ollama.invalid"
	cfg.Model = "qwen3:14b"
	cfg.HTTP = &http.Client{Transport: rt}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

func drain(t *testing.T, ch <-chan session.Action) []session.Action {
	t.Helper()
	var got []session.Action
	for a := range ch {
		got = append(got, a)
	}
	return got
}

// The whole point of the seam: a wire stream becomes session actions.
//
// verifies SPEC §12
func TestTurnStreamsTheDecodedActions(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "speak_then_slow_tool.ndjson")}
	e := engineOn(t, rt, Config{})

	ch, err := e.Turn(context.Background(), session.Input{
		ConversationID: "conv-1", Speaker: "alan", Text: "find me a lasagne recipe",
	})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if got, want := kinds(drain(t, ch)), []string{"speech", "call:media_search", "end"}; !equal(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
}

// The request is the only place the protocol is explained, so these clauses
// are load-bearing: every one of them was added because a model got it wrong
// without it.
//
// verifies SPEC §4.1, §14
func TestTheRequestTellsTheModelHowToSpeak(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "reply.ndjson")}
	e := engineOn(t, rt, Config{})
	ch, err := e.Turn(context.Background(), session.Input{Text: "hello"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)

	var got struct {
		Model    string     `json:"model"`
		Stream   bool       `json:"stream"`
		Think    *bool      `json:"think"`
		Messages []message  `json:"messages"`
		Tools    []wireTool `json:"tools"`
	}
	if err := json.Unmarshal(rt.reqBody, &got); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	if !got.Stream {
		t.Error("the turn must stream: a buffered reply cannot start speaking early")
	}
	// Absent, not false: a model that does not support thinking answers 400.
	if got.Think != nil {
		t.Error("think must be omitted unless configured")
	}
	if got.Model != "qwen3:14b" {
		t.Errorf("model = %q", got.Model)
	}

	sys := got.Messages[0].Content
	for _, want := range []string{"speak tool", "acknowledgement", "end_session"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt omits %q", want)
		}
	}

	byName := map[string]wireFunction{}
	for _, tool := range got.Tools {
		if tool.Type != "function" {
			t.Errorf("%s has type %q, want function", tool.Function.Name, tool.Type)
		}
		byName[tool.Function.Name] = tool.Function
	}
	speak, ok := byName["speak"]
	if !ok {
		t.Fatalf("speak is not offered; tools are %v", byName)
	}
	if got, want := speak.Parameters.Required, []string{"text"}; !equal(got, want) {
		t.Errorf("speak required = %v, want %v", got, want)
	}
	if got, want := speak.Parameters.Properties["mode"].Enum, []string{"queue", "preempt", "interject"}; !equal(got, want) {
		t.Errorf("mode enum = %v, want %v", got, want)
	}
	// A slow tool that does not say so gets called without a speak first, and
	// the user waits in silence.
	if d := byName["media_search"].Description; !strings.Contains(d, "several seconds") {
		t.Errorf("media_search description omits its latency: %q", d)
	}
	// A no-param tool still needs an object schema, not a null one.
	if s := byName["end_session"].Parameters; s.Type != "object" || s.Properties == nil {
		t.Errorf("end_session schema = %+v", s)
	}
}

// Policy is the orchestrator's, enforced from the same declaration. Sending it
// invites the model to reason about limits it does not set.
//
// verifies SPEC §6
func TestPolicyIsNotOfferedToTheModel(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "reply.ndjson")}
	e := engineOn(t, rt, Config{})
	ch, err := e.Turn(context.Background(), session.Input{Text: "hello"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)

	for _, leaked := range []string{"on_interrupt", "timeout_ms", "requires_confirmation", "unknown_speaker", "scope"} {
		if strings.Contains(string(rt.reqBody), leaked) {
			t.Errorf("request leaks policy field %q to the model", leaked)
		}
	}
}

// The transcript is training data and a label prefixed onto it is something a
// small model reads back out loud.
//
// verifies SPEC §5
func TestTheSpeakerIsNamedOutsideTheTranscript(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "reply.ndjson")}
	e := engineOn(t, rt, Config{})
	ch, err := e.Turn(context.Background(), session.Input{Speaker: "alan", Text: "turn the lights off"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)

	var got struct {
		Messages []message `json:"messages"`
	}
	if err := json.Unmarshal(rt.reqBody, &got); err != nil {
		t.Fatal(err)
	}
	user := got.Messages[len(got.Messages)-1]
	if user.Role != "user" || user.Content != "turn the lights off" {
		t.Errorf("transcript was altered: %+v", user)
	}
	if !strings.Contains(got.Messages[0].Content, "alan") {
		t.Error("the speaker is not named to the model at all")
	}
}

// The room is the satellite's, told beside who is speaking and never in
// their words: "turn the lights off" heard in the kitchen means the
// kitchen's, and a model that is not told so has to guess or ask.
//
// verifies SPEC §5
func TestTheRoomIsToldOutsideTheTranscript(t *testing.T) {
	for _, tc := range []struct {
		room string
		want string
	}{
		{room: "kitchen", want: "satellite in the kitchen"},
		{room: "", want: ""},
	} {
		rt := &roundTrip{body: fixture(t, "reply.ndjson")}
		e := engineOn(t, rt, Config{})
		ch, err := e.Turn(context.Background(), session.Input{Speaker: "teagan", Room: tc.room, Text: "turn the lights off"})
		if err != nil {
			t.Fatalf("turn: %v", err)
		}
		drain(t, ch)

		var got struct {
			Messages []message `json:"messages"`
		}
		if err := json.Unmarshal(rt.reqBody, &got); err != nil {
			t.Fatal(err)
		}
		sys, user := got.Messages[0].Content, got.Messages[len(got.Messages)-1]
		if user.Content != "turn the lights off" {
			t.Errorf("room %q: transcript was altered: %+v", tc.room, user)
		}
		if tc.want != "" && !strings.Contains(sys, tc.want) {
			t.Errorf("room %q: system prompt does not say where it is speaking from:\n%s", tc.room, sys)
		}
		if tc.want == "" && strings.Contains(sys, "speaking through") {
			t.Errorf("no room, yet the system prompt names one:\n%s", sys)
		}
	}
}

// A turn that never started is the error return's job. The status alone does
// not say what went wrong: an unsupported think field is a 400 that only the
// body names.
func TestARejectedRequestFailsTheTurn(t *testing.T) {
	rt := &roundTrip{
		status: http.StatusBadRequest,
		body:   `{"error":"\"llama3.1:8b\" does not support thinking"}`,
	}
	yes := true
	e := engineOn(t, rt, Config{Think: &yes})

	ch, err := e.Turn(context.Background(), session.Input{Text: "hello"})
	if err == nil {
		t.Fatal("a 400 must fail the turn")
	}
	if ch != nil {
		t.Error("a failed turn must not hand back a channel to drain")
	}
	if !strings.Contains(err.Error(), "does not support thinking") {
		t.Errorf("err = %v, want the endpoint's explanation", err)
	}

	// Think is sent only when set, and then as sent.
	var got struct {
		Think *bool `json:"think"`
	}
	if err := json.Unmarshal(rt.reqBody, &got); err != nil {
		t.Fatal(err)
	}
	if got.Think == nil || !*got.Think {
		t.Errorf("think = %v, want it sent as configured", got.Think)
	}
}

func TestADeadEndpointFailsTheTurn(t *testing.T) {
	rt := &roundTrip{err: errors.New("dial tcp: connection refused")}
	e := engineOn(t, rt, Config{})
	if _, err := e.Turn(context.Background(), session.Input{Text: "hello"}); err == nil {
		t.Fatal("a dial failure must fail the turn")
	}
}

// The journal refuses a model_completed without complete versions, so a turn
// this engine produces has to be recordable end to end.
//
// verifies SPEC §8
func TestATurnIsRecordableInTheJournal(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "speak_then_slow_tool.ndjson")}
	e := engineOn(t, rt, Config{})
	ch, err := e.Turn(context.Background(), session.Input{Text: "find me a lasagne recipe"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	acts := drain(t, ch)
	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want TurnEnd", acts[len(acts)-1])
	}

	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), e.Versions())
	if _, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind: journal.KindModelCompleted,
		Fields: map[string]string{
			"completion_json": end.Completion,
			"finish_reason":   end.FinishReason,
		},
	}); err != nil {
		t.Fatalf("the journal rejected a successful turn: %v", err)
	}
}

// Versions decide whether two traces are comparable, so they must follow what
// was actually sent rather than a number somebody remembers to bump.
//
// verifies SPEC §8, §13
func TestVersionsFollowWhatWasSent(t *testing.T) {
	base := engineOn(t, &roundTrip{}, Config{})
	v := base.Versions()
	if v.Model == "" || v.Prompt == "" || v.ToolSchema == "" {
		t.Fatalf("versions incomplete: %+v", v)
	}

	// Same inputs, same versions: a hash that moved on its own would make every
	// trace incomparable to the last.
	if again := engineOn(t, &roundTrip{}, Config{}).Versions(); again != v {
		t.Errorf("versions are not stable: %+v then %+v", v, again)
	}

	edited := engineOn(t, &roundTrip{}, Config{Prompt: DefaultPrompt + " Be terse."}).Versions()
	if edited.Prompt == v.Prompt {
		t.Error("an edited prompt kept the old version")
	}
	// The tool schema did not change, so its version must not either, or no
	// replay can tell which half of a pair moved.
	if edited.ToolSchema != v.ToolSchema {
		t.Error("editing the prompt changed the tool schema version")
	}
}

func TestNewRejectsIncompleteWiring(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no url":   {Model: "qwen3:14b"},
		"no model": {BaseURL: "http://ollama.invalid"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
