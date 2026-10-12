// Package vllm is the turn engine on vLLM's OpenAI-compatible
// /v1/chat/completions, decoded into the session's actions as they stream
// (SPEC §10, §12, ADR-0077).
//
// SPEC §10 picks vLLM because it parses tool calls as they stream. The cost
// is the shape it streams them in: a call's arguments arrive as fragments
// of a JSON string, keyed by the call's index, which Ollama's /api/chat
// spares its engine. So this package reassembles them, dispatches each call
// the moment its JSON closes, and reads a speak call's words out of its
// arguments while they are still arriving, so speech starts before the
// call, let alone the turn, has ended (SPEC §4.1).
//
// What the model is told -- the persona, the system message, the dialogue
// and the tool declarations -- is internal/provider/chat's, shared with the
// Ollama engine, so the prompt and tool-schema versions an event records do
// not say which endpoint asked.
package vllm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// errBody bounds how much of a failed response is quoted back. Enough for the
// endpoint's message, not enough to put a whole HTML error page in a log line.
const errBody = 4 << 10

// Config wires an Engine. Everything that does I/O is injected, so `task test`
// stays hermetic (CONTRIBUTING).
type Config struct {
	// BaseURL is the server's root, e.g. http://vllm.lan:8000. A trailing /v1,
	// as OpenAI clients are configured, is accepted too.
	BaseURL string
	// Model is the name the server serves the model as: its --served-model-name,
	// or the Hugging Face id it was started with. It is recorded against
	// every event.
	Model string
	// APIKey is sent as a bearer token when set: the server's --api-key.
	APIKey string

	// HTTP defaults to a client with no timeout, deliberately. A turn streams
	// for as long as the model talks, so Client.Timeout would cut speech off
	// mid-utterance; the turn context is what bounds a turn (SPEC §4.4).
	HTTP *http.Client

	// Prompt defaults to chat.DefaultPrompt. Replacing it changes the Prompt
	// version recorded against every event, which is the point (SPEC §13).
	Prompt string

	// Specs defaults to registry.Offered: the generated declaration the
	// orchestrator also enforces policy from, less the deferred tools
	// nothing runs (ADR-0060).
	Specs map[string]registry.ToolSpec

	// Thinking turns Qwen3's reasoning on, through its chat template's
	// enable_thinking. Off by default: reasoning is generated before the
	// first call, and measured on qwen3:14b it cost 3.5-4.2 s, against SPEC
	// §11's ~700 ms to first audio. A template that has no such switch
	// ignores it.
	Thinking bool

	// MaxTokens caps one ask's completion. Zero leaves it to the server,
	// which allows whatever the context has left.
	MaxTokens int

	// Location is the household's time zone: what "it is 8 AM" and
	// "yesterday" are told in. Nil is the daemon's local zone.
	Location *time.Location
}

// Engine turns one ask into one streamed /v1/chat/completions turn. The
// request carries the system prompt and the dialogue the session derived
// from the log; deciding when to ask again is the session's (ADR-0037).
type Engine struct {
	cfg      Config
	url      string
	tools    []chat.Tool
	versions journal.Versions
}

// Engine is the seam phase 2's speech-to-speech provider replaces (SPEC §12).
var (
	_ session.Engine     = (*Engine)(nil)
	_ session.Summarizer = (*Engine)(nil)
)

// New validates the wiring and fingerprints what the model will be sent,
// without touching the network.
func New(cfg Config) (*Engine, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("vllm: base url is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("vllm: model is required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.Prompt == "" {
		cfg.Prompt = chat.DefaultPrompt
	}
	if cfg.Specs == nil {
		cfg.Specs = registry.Offered()
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	root := strings.TrimSuffix(strings.TrimSuffix(cfg.BaseURL, "/"), "/v1")
	return &Engine{
		cfg:   cfg,
		url:   root + "/v1/chat/completions",
		tools: chat.Tools(cfg.Specs),
		versions: journal.Versions{
			Model:      cfg.Model,
			Prompt:     chat.PromptVersion(cfg.Prompt),
			ToolSchema: chat.ToolSchemaVersion(cfg.Specs),
		},
	}, nil
}

// Versions attributes every event of a turn. The journal refuses to record a
// model_completed without them. They hash what is sent, which is the same
// prompt and declarations the Ollama engine sends, so a trace from either is
// comparable to the other's but for the model (SPEC §8, §13).
func (e *Engine) Versions() journal.Versions { return e.versions }

// message is one chat message in OpenAI's shape. An assistant message
// carries the calls the model made; a tool message carries one call's
// result, named by the call's id, which is how this endpoint matches them.
type message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []wireCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// wireCall is one call as this endpoint frames it, in a request and in a
// streamed delta alike. Arguments is a JSON string, not an object: in a
// stream, fragments of one.
type wireCall struct {
	// Index keys a streamed delta to its call. Absent from a request.
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// request is one POST /v1/chat/completions body.
type request struct {
	Model      string      `json:"model"`
	Messages   []message   `json:"messages"`
	Tools      []chat.Tool `json:"tools,omitempty"`
	ToolChoice string      `json:"tool_choice,omitempty"`
	Stream     bool        `json:"stream"`
	MaxTokens  int         `json:"max_tokens,omitempty"`

	// ChatTemplateKwargs reaches the model's chat template: Qwen3's
	// enable_thinking is how its reasoning is turned off, since vLLM has no
	// request field of its own for it.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
}

// templateKwargs is what the chat template is told: always enable_thinking,
// so the server's default never decides it.
func (e *Engine) templateKwargs() map[string]any {
	return map[string]any{"enable_thinking": e.cfg.Thinking}
}

// Turn opens the stream and decodes it as it arrives. The returned error is
// only for a turn that never started; a stream that fails later ends the turn
// on the channel instead.
func (e *Engine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	// The goroutine below owns the body once the turn starts; bodyclose
	// cannot see into it.
	resp, err := e.post(ctx, request{ //nolint:bodyclose
		Model:              e.cfg.Model,
		Messages:           e.messages(in),
		Tools:              e.tools,
		ToolChoice:         "auto",
		Stream:             true,
		MaxTokens:          e.cfg.MaxTokens,
		ChatTemplateKwargs: e.templateKwargs(),
	}, "chat")
	if err != nil {
		return nil, err
	}
	out := make(chan session.Action)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		// The error is deliberately not returned anywhere: decode has already
		// ended the turn with it, and the journal is where this runtime reports
		// itself (SPEC §8).
		_ = decoder{}.decode(ctx, resp.Body, out)
	}()
	return out, nil
}

// post sends one request and returns a 200's response for the caller to
// read and close. Anything else is an error carrying the server's own
// explanation, which the status alone does not give: a model the server
// does not serve and a template that refuses the dialogue are both a 400
// (or a 404) that only the body names.
func (e *Engine) post(ctx context.Context, body request, what string) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("vllm: encode %s request: %w", what, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("vllm: build %s request: %w", what, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if body.Stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if e.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.APIKey)
	}
	// A streamed body outlives this call, so it cannot be closed here. Both
	// paths close it: the error path below, and the caller once it has a 200.
	resp, err := e.cfg.HTTP.Do(req) //nolint:bodyclose
	if err != nil {
		return nil, fmt.Errorf("vllm %s: %w", what, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return nil, fmt.Errorf("vllm %s: %s: %s", what, resp.Status, strings.TrimSpace(string(msg)))
	}
	return resp, nil
}

// messages is what the model sees: the system message chat.System builds,
// then the dialogue, each step in this endpoint's shape.
func (e *Engine) messages(in session.Input) []message {
	out := []message{{Role: "system", Content: chat.System(e.cfg.Prompt, in, e.cfg.Location)}}
	if len(in.Dialogue) == 0 {
		// Asked a turn on its own, as Replay's re-runs are.
		return append(out, message{Role: "user", Content: in.Text})
	}
	for _, s := range chat.Dialogue(in.Dialogue) {
		m := message{Role: s.Role, Content: s.Content}
		if s.Role == "tool" {
			m.ToolCallID = s.CallID
		}
		for _, c := range s.Calls {
			var w wireCall
			w.ID, w.Type = c.ID, "function"
			w.Function.Name, w.Function.Arguments = c.Name, string(c.Args)
			m.ToolCalls = append(m.ToolCalls, w)
		}
		out = append(out, m)
	}
	return out
}
