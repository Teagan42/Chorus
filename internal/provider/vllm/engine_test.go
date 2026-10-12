package vllm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// sentRequest is a request body as the server read it.
type sentRequest struct {
	Model              string         `json:"model"`
	Stream             bool           `json:"stream"`
	ToolChoice         string         `json:"tool_choice"`
	MaxTokens          int            `json:"max_tokens"`
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs"`
	Messages           []message      `json:"messages"`
	Tools              []chat.Tool    `json:"tools"`
}

func decodeSent(t *testing.T, s served) sentRequest {
	t.Helper()
	var r sentRequest
	if err := json.Unmarshal(s.body, &r); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, s.body)
	}
	return r
}

// ask runs one turn against a server answering with stream, and returns
// what it did and what it was sent.
func ask(t *testing.T, cfg Config, in session.Input, stream string) ([]session.Action, sentRequest) {
	t.Helper()
	f, srv := newFake(t, stream)
	ch, err := engineAt(t, srv, cfg).Turn(context.Background(), in)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	acts := drain(t, ch)
	return acts, decodeSent(t, f.last(t))
}

// The whole point of the seam: vLLM's stream becomes session actions, over
// HTTP, as the session reads them.
//
// verifies SPEC §12
func TestTurnStreamsTheDecodedActions(t *testing.T) {
	acts, _ := ask(t, Config{}, session.Input{ConversationID: "conv-1", Speaker: "alan", Room: "kitchen", Text: "turn off the kitchen and porch lights"}, fixture(t, "speak_and_two_lights.sse"))
	want := []string{"speech", "speech", "speech", "speech:last", "call:ha_call_service", "call:ha_call_service", "end:tool_calls"}
	if got := kinds(acts); !slices.Equal(got, want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
}

// The request asks for a stream with tools the server parses itself, and
// tells Qwen3's template not to think unless configured to: the reasoning
// it generates first is time the household spends in silence (SPEC §11).
//
// verifies SPEC §4.1, §11
func TestTheRequestStreamsToolsWithThinkingOff(t *testing.T) {
	f, srv := newFake(t, fixture(t, "reply.sse"))
	ch, err := engineAt(t, srv, Config{Specs: registry.Specs, APIKey: "sk-household-7f3a"}).Turn(context.Background(), session.Input{Text: "hello"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)

	s := f.last(t)
	if s.path != "/v1/chat/completions" {
		t.Errorf("path = %q", s.path)
	}
	if got := s.header.Get("Authorization"); got != "Bearer sk-household-7f3a" {
		t.Errorf("authorization = %q, want the configured key as a bearer token", got)
	}
	got := decodeSent(t, s)
	if !got.Stream {
		t.Error("the turn must stream: a buffered reply cannot start speaking early")
	}
	if got.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want auto, which the server's tool parser answers", got.ToolChoice)
	}
	if got.Model != "Qwen/Qwen3-32B-AWQ" {
		t.Errorf("model = %q", got.Model)
	}
	if think, ok := got.ChatTemplateKwargs["enable_thinking"]; !ok || think != false {
		t.Errorf("chat_template_kwargs = %v, want enable_thinking false", got.ChatTemplateKwargs)
	}
	if got.MaxTokens != 0 {
		t.Errorf("max_tokens = %d, want it left to the server", got.MaxTokens)
	}

	sys := got.Messages[0]
	if sys.Role != "system" {
		t.Fatalf("first message is %q", sys.Role)
	}
	for _, want := range []string{"speak tool", "acknowledgement", "end_session"} {
		if !strings.Contains(sys.Content, want) {
			t.Errorf("system prompt omits %q", want)
		}
	}
	byName := map[string]chat.Function{}
	for _, tool := range got.Tools {
		byName[tool.Function.Name] = tool.Function
	}
	if got, want := byName["speak"].Parameters.Required, []string{"text"}; !slices.Equal(got, want) {
		t.Errorf("speak required = %v, want %v", got, want)
	}
	if d := byName["media_search"].Description; !strings.Contains(d, "several seconds") {
		t.Errorf("media_search description omits its latency: %q", d)
	}
	// Policy is the orchestrator's, enforced from the same declaration.
	for _, leaked := range []string{"on_interrupt", "timeout_ms", "requires_confirmation", "unknown_speaker"} {
		if strings.Contains(string(s.body), leaked) {
			t.Errorf("request leaks policy field %q to the model", leaked)
		}
	}
}

// Thinking on is asked for as configured, and a cap on the completion is
// sent when one is set.
//
// verifies SPEC §11
func TestThinkingAndACapAreSentAsConfigured(t *testing.T) {
	_, got := ask(t, Config{Thinking: true, MaxTokens: 512}, session.Input{Text: "hello"}, fixture(t, "reply.sse"))
	if got.ChatTemplateKwargs["enable_thinking"] != true || got.MaxTokens != 512 {
		t.Errorf("chat_template_kwargs = %v, max_tokens = %d", got.ChatTemplateKwargs, got.MaxTokens)
	}
}

// An OpenAI client is configured with a base URL ending in /v1; one
// configured the same way here asks the same endpoint, not /v1/v1.
func TestABaseURLEndingInV1AsksTheSameEndpoint(t *testing.T) {
	f, srv := newFake(t, fixture(t, "reply.sse"))
	e, err := New(Config{BaseURL: srv.URL + "/v1/", Model: "Qwen/Qwen3-32B-AWQ", HTTP: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ch, err := e.Turn(context.Background(), session.Input{Text: "hello"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)
	if p := f.last(t).path; p != "/v1/chat/completions" {
		t.Errorf("path = %q", p)
	}
}

// The follow-up after Teagan's garage question, in OpenAI's shape: the
// calls go back as the model made them, arguments as a JSON string, and
// each result names the call it answers by id, which is how this endpoint
// pairs them. The words of the speak call are the heard ones.
//
// verifies SPEC §4.1, §4.4
func TestAFollowUpCarriesTheCallsAndWhatTheyReturned(t *testing.T) {
	_, got := ask(t, Config{}, session.Input{
		Speaker: "teagan",
		Text:    "is the garage door closed",
		Dialogue: []journal.Entry{
			{Kind: journal.EntryHeard, Text: "is the garage door closed"},
			{Kind: journal.EntrySaid, CallID: "chatcmpl-tool-8b2e6a0d4f9c4715", Text: "Let me check the garage door.", Pending: true},
			{Kind: journal.EntryCall, CallID: "chatcmpl-tool-3f7c1a5e9d2b4860", Tool: "ha_get_state", Args: `{"entity_id": "cover.garage_door"}`},
			{Kind: journal.EntryResult, CallID: "chatcmpl-tool-3f7c1a5e9d2b4860", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
		},
	}, fixture(t, "reply.sse"))

	call := func(id, name, args string) wireCall {
		var w wireCall
		w.ID, w.Type = id, "function"
		w.Function.Name, w.Function.Arguments = name, args
		return w
	}
	want := []message{
		{Role: "user", Content: "is the garage door closed"},
		{Role: "assistant", ToolCalls: []wireCall{call("chatcmpl-tool-8b2e6a0d4f9c4715", "speak", `{"text":"Let me check the garage door."}`)}},
		{Role: "tool", ToolCallID: "chatcmpl-tool-8b2e6a0d4f9c4715", Content: `{"playing":true}`},
		{Role: "assistant", ToolCalls: []wireCall{call("chatcmpl-tool-3f7c1a5e9d2b4860", "ha_get_state", `{"entity_id": "cover.garage_door"}`)}},
		{Role: "tool", ToolCallID: "chatcmpl-tool-3f7c1a5e9d2b4860", Content: `{"entity_id":"cover.garage_door","state":"open"}`},
	}
	if !reflect.DeepEqual(got.Messages[1:], want) {
		t.Errorf("messages =\n%+v\nwant\n%+v", got.Messages[1:], want)
	}
}

// Who is speaking, where, and when are told in the system message, never in
// the person's words, exactly as the Ollama engine tells them: the message
// is the shared one, so swapping the endpoint does not change it.
//
// verifies SPEC §5
func TestTheSystemMessageIsTheOneEveryEngineSends(t *testing.T) {
	pdt := time.FixedZone("PDT", -7*3600)
	in := session.Input{
		Speaker: "teagan", Room: "kitchen", Text: "turn the lights off", Now: time.Date(2026, 10, 9, 15, 53, 0, 0, time.UTC),
		Memories: []journal.Memory{{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee."}},
	}
	_, got := ask(t, Config{Location: pdt}, in, fixture(t, "reply.sse"))
	if sys := got.Messages[0].Content; sys != chat.System(chat.DefaultPrompt, in, pdt) {
		t.Errorf("system message =\n%s\nwant chat.System's", sys)
	}
	for _, want := range []string{"You are speaking with teagan.", "satellite in the kitchen", "It is Friday 9 October 2026, 08:53 PDT.", `- m_3f9c2a10: "Takes oat milk in coffee."`} {
		if !strings.Contains(got.Messages[0].Content, want) {
			t.Errorf("system message lacks %q", want)
		}
	}
	if user := got.Messages[len(got.Messages)-1]; user.Role != "user" || user.Content != "turn the lights off" {
		t.Errorf("transcript was altered: %+v", user)
	}
}

// The versions hash what is sent, which is what the Ollama engine sends: a
// trace from either is comparable to the other's but for the model, and
// the contexts archive already holds their text (ADR-0071).
//
// verifies SPEC §8, §13
func TestVersionsAreTheOnesTheOllamaEngineRecords(t *testing.T) {
	e, err := New(Config{BaseURL: "http://vllm.invalid:8000", Model: "Qwen/Qwen3-32B-AWQ"})
	if err != nil {
		t.Fatal(err)
	}
	o, err := ollama.New(ollama.Config{BaseURL: "http://ollama.invalid:11434", Model: "qwen3:32b"})
	if err != nil {
		t.Fatal(err)
	}
	v, ov := e.Versions(), o.Versions()
	if v.Model != "Qwen/Qwen3-32B-AWQ" || v.Prompt != ov.Prompt || v.ToolSchema != ov.ToolSchema {
		t.Errorf("versions = %+v, Ollama's = %+v", v, ov)
	}
	edited, err := New(Config{BaseURL: "http://vllm.invalid:8000", Model: "Qwen/Qwen3-32B-AWQ", Prompt: chat.DefaultPrompt + " Be terse."})
	if err != nil {
		t.Fatal(err)
	}
	if ev := edited.Versions(); ev.Prompt == v.Prompt || ev.ToolSchema != v.ToolSchema {
		t.Errorf("an edited prompt: %+v against %+v", ev, v)
	}
}

// The journal refuses a model_completed without complete versions, so a
// turn this engine produces has to be recordable end to end.
//
// verifies SPEC §8
func TestATurnIsRecordableInTheJournal(t *testing.T) {
	_, srv := newFake(t, fixture(t, "speak_and_two_lights.sse"))
	e := engineAt(t, srv, Config{})
	ch, err := e.Turn(context.Background(), session.Input{Text: "turn off the kitchen and porch lights"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	end := endOf(t, drain(t, ch))
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), e.Versions())
	if _, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind:   journal.KindModelCompleted,
		Fields: map[string]string{"completion_json": end.Completion, "finish_reason": end.FinishReason},
	}); err != nil {
		t.Fatalf("the journal rejected a successful turn: %v", err)
	}
	var comp completion
	if err := json.Unmarshal([]byte(end.Completion), &comp); err != nil || len(comp.ToolCalls) != 3 {
		t.Errorf("completion = %s, want all three calls kept", end.Completion)
	}
}

// A turn that never started is the error return's job, with the server's
// own explanation: a model it does not serve is a 404 only the body names.
//
// verifies SPEC §7
func TestARejectedRequestFailsTheTurn(t *testing.T) {
	f, srv := newFake(t, `{"error":{"message":"The model `+"`qwen3:32b`"+` does not exist.","type":"NotFoundError","param":null,"code":404}}`)
	f.status = http.StatusNotFound
	ch, err := engineAt(t, srv, Config{Model: "qwen3:32b"}).Turn(context.Background(), session.Input{Text: "hello"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want the server's explanation", err)
	}
	if ch != nil {
		t.Error("a failed turn must not hand back a channel to drain")
	}
}

func TestADeadEndpointFailsTheTurn(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	e, err := New(Config{BaseURL: url, Model: "Qwen/Qwen3-32B-AWQ"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Turn(context.Background(), session.Input{Text: "hello"}); err == nil {
		t.Fatal("a dial failure must fail the turn")
	}
}

// heldStream answers with the stream up to the first cut, then holds the
// response open until release closes or the client goes away, then sends
// the rest.
func heldStream(stream, upTo string, release <-chan struct{}) http.HandlerFunc {
	cut := strings.Index(stream, upTo)
	cut += strings.Index(stream[cut:], "\n\n") + 2
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream[:cut])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, stream[cut:])
	}
}

// Speech must start before the turn ends: the first words of "Turning off
// the kitchen and porch lights." reach the session while the server is
// still holding the rest of the stream, the speak call unfinished and the
// light calls not yet made.
//
// verifies SPEC §4.1
func TestSpeechStartsBeforeTheTurnEnds(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(heldStream(fixture(t, "speak_and_two_lights.sse"), "Turning off the kit", release))
	t.Cleanup(srv.Close)
	ch, err := engineAt(t, srv, Config{}).Turn(context.Background(), session.Input{Text: "turn off the kitchen and porch lights"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	first := <-ch
	if d, ok := first.(session.SpeechDelta); !ok || d.Text != "Turning off the kit" || d.Last {
		t.Fatalf("first action = %+v, want the first words, unfinished", first)
	}
	close(release)
	if got := kinds(drain(t, ch)); !slices.Equal(got, []string{"speech", "speech", "speech:last", "call:ha_call_service", "call:ha_call_service", "end:tool_calls"}) {
		t.Errorf("the rest = %v", got)
	}
}

// A barge-in cancels the turn while the model is mid-speech. The request is
// abandoned, the utterance it opened is closed, and the turn still reports
// its end; the server sees the client go.
//
// verifies SPEC §4.4
func TestABargeInMidSpeechCancelsTheRequest(t *testing.T) {
	gone := make(chan struct{})
	stream := fixture(t, "speak_and_two_lights.sse")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(gone)
		heldStream(stream, "Turning off the kit", nil)(w, r)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := engineAt(t, srv, Config{}).Turn(ctx, session.Input{Text: "turn off the kitchen and porch lights"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	if d, ok := (<-ch).(session.SpeechDelta); !ok || d.Last {
		t.Fatalf("first action = %+v, want words still streaming", d)
	}
	cancel()
	acts := drain(t, ch)
	if got := kinds(acts); !slices.Equal(got, []string{"speech:last", "end:error"}) {
		t.Errorf("after the barge-in: %v, want the utterance closed and the end", got)
	}
	if d := speechOf(acts)[0]; d.CallID != "chatcmpl-tool-5d1e0b9a4c7f4e2b" || d.Text != "" {
		t.Errorf("closing delta = %+v", d)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the server never saw the request abandoned")
	}
}

func TestNewRejectsIncompleteWiring(t *testing.T) {
	for name, cfg := range map[string]Config{
		"no url":   {Model: "Qwen/Qwen3-32B-AWQ"},
		"no model": {BaseURL: "http://vllm.invalid:8000"},
	} {
		if _, err := New(cfg); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
