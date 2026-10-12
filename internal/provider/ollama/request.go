package ollama

import (
	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/registry"
)

// DefaultPrompt is the persona and protocol the model is given. SPEC §13 keeps
// this as a versioned artifact: prompt versions are recorded per event, so a
// persona change is A/B-testable against replayed traces. Every engine sends
// the same one, so the endpoint is not part of what the model is told;
// chat.DefaultPrompt says which of its clauses were measured, and on what.
const DefaultPrompt = chat.DefaultPrompt

// message is one chat message. An assistant message carries the calls the
// model made; a tool message carries one call's result, named by its tool,
// which is how this endpoint matches the two.
type message struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`
	ToolName  string     `json:"tool_name,omitempty"`
}

// request is one POST /api/chat body.
type request struct {
	Model    string     `json:"model"`
	Messages []message  `json:"messages"`
	Tools    []wireTool `json:"tools,omitempty"`
	Stream   bool       `json:"stream"`

	// Think is a pointer because a model that does not support it answers 400
	// rather than ignoring it: llama3.1 and every model under 9B tried so far
	// reject the field outright, so it has to be absent, not false.
	Think *bool `json:"think,omitempty"`

	// KeepAlive holds the model resident between turns. A cold load costs ~71 s
	// against SPEC §11's ~700 ms first-audio budget.
	KeepAlive string `json:"keep_alive,omitempty"`
}

// wireTool is a tool in this endpoint's shape, which is OpenAI's rather than
// the generated artifact's `input_schema` one.
type (
	wireTool     = chat.Tool
	wireFunction = chat.Function
)

// wireTools maps the generated declaration onto the wire: the same registry
// always marshals to the same bytes, which the tool-schema version hashes
// (SPEC §8).
func wireTools(specs map[string]registry.ToolSpec) []wireTool { return chat.Tools(specs) }
