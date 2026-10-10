package ollama

import (
	"sort"

	"github.com/teaganglenn/chorus/internal/registry"
)

// DefaultPrompt is the persona and protocol the model is given. SPEC §13 keeps
// this as a versioned artifact: prompt versions are recorded per event, so a
// persona change is A/B-testable against replayed traces.
//
// Three clauses are measured rather than stylistic:
//
// Speech is tool-only because content is not speech here. With think:false,
// qwen3:4b streams its reasoning as content and phi4-mini echoed the entire
// tool schema there. The decoder drops content while a turn streams (SPEC
// §4.1). Told the garage code as a memory, qwen3:14b still reasoned that it
// needed no tools since it already knew, and wrote "speak\nThe garage door
// code is 4512." as content; hence "saying it is still a speak call". It
// still does so often enough that a turn calling nothing, whose reasoning
// came separately, has its content spoken at the end (decoder.answer,
// ADR-0046). The tool is still asked for: content beside a call, or cut off
// by its length, is never spoken.
//
// The acknowledgement clause replaced an ordering one. Told to call speak
// FIRST and the slow tool after, qwen3:14b and ornith:9b did so some of the
// time and called the tool alone the rest, however it was worded. The words
// to say are now a required argument of the slow tool itself, which the
// session speaks as the call starts, so no ordering is left to get wrong
// (ADR-0039).
//
// Brevity is here because one speak call carries a whole paragraph in one
// delta, which is the unit a barge-in has to truncate (SPEC §15 item 1).
const DefaultPrompt = `You are a voice assistant in a home. Everything you say is spoken aloud, so keep replies short and plain -- no lists, no markdown, no emoji.

Speak only by calling the speak tool. That holds when you already know the answer, from what you remember or anywhere else: saying it is still a speak call. Never answer in ordinary message content, and never write a tool's name there.

A tool that takes a moment asks for an acknowledgement: the few words the person hears while it works. They are spoken for you as it starts, so do not also call speak to say them.

Call end_session once the conversation is finished.`

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
type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Parameters  wireSchema `json:"parameters"`
}

type wireSchema struct {
	Type string `json:"type"`
	// Never omitted: a tool with no parameters still needs an object schema,
	// and some models answer a null one by inventing arguments.
	Properties map[string]wireProp `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type wireProp struct {
	Type        string    `json:"type"`
	Description string    `json:"description,omitempty"`
	Enum        []string  `json:"enum,omitempty"`
	Items       *wireProp `json:"items,omitempty"`
}

// wireTools maps the generated declaration onto the wire. Name-sorted, and
// required within a tool kept in declaration order, so the same registry always
// marshals to the same bytes -- which is what lets the tool-schema version be a
// hash of them (SPEC §8).
func wireTools(specs map[string]registry.ToolSpec) []wireTool {
	names := make([]string, 0, len(specs))
	for name := range specs {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]wireTool, 0, len(names))
	for _, name := range names {
		spec := specs[name]
		props := make(map[string]wireProp, len(spec.ModelParams))
		var required []string
		for _, p := range spec.ModelParams {
			prop := wireProp{Type: p.Type, Description: p.Description, Enum: p.Enum}
			if p.Items != "" {
				prop.Items = &wireProp{Type: p.Items}
			}
			props[p.Name] = prop
			if p.Required {
				required = append(required, p.Name)
			}
		}
		out = append(out, wireTool{
			Type: "function",
			Function: wireFunction{
				Name: spec.Name,
				// ModelDescription, not Description: a slow tool has to tell the
				// model it is slow or the model calls it without speaking first.
				Description: spec.ModelDescription,
				Parameters:  wireSchema{Type: "object", Properties: props, Required: required},
			},
		})
	}
	return out
}
