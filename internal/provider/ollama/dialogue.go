package ollama

import (
	"encoding/json"

	"github.com/teaganglenn/chorus/internal/journal"
)

// dialogue renders the conversation the session derived from the log as
// /api/chat messages, in order (SPEC §4.4).
//
// Speech goes back as the speak calls the model made, never as assistant
// content: the prompt tells the model content is never heard, and a history
// that answers in content teaches it otherwise. Each speak carries the words
// the person heard, and its result says whether they heard all of them.
func dialogue(entries []journal.Entry) []message {
	var out []message
	for _, e := range entries {
		switch e.Kind {
		case journal.EntryHeard:
			out = append(out, message{Role: "user", Content: e.Text})
		case journal.EntrySaid:
			if e.Text == "" {
				// Streamed speech still playing whose words are not known
				// yet: nothing to tell the model it said.
				continue
			}
			// The words alone: the mode the model chose is the speech
			// channel's business, and an empty one is not in the enum.
			args, _ := json.Marshal(map[string]string{"text": e.Text})
			out = calling(out, e.CallID, toolSpeak, args)
			out = append(out, message{Role: "tool", ToolName: toolSpeak, Content: heard(e)})
		case journal.EntryCall:
			args := json.RawMessage(e.Args)
			if !json.Valid(args) {
				// Recorded verbatim from a model that wrote broken JSON. The
				// result says the call failed; resending it would fail the ask.
				args = json.RawMessage("{}")
			}
			out = calling(out, e.CallID, e.Tool, args)
		case journal.EntryResult:
			out = append(out, message{Role: "tool", ToolName: e.Tool, Content: result(e)})
		}
	}
	return out
}

// calling adds a call to the assistant message that is still open, or opens
// one: calls the model made together go back together.
func calling(out []message, id, name string, args json.RawMessage) []message {
	var tc toolCall
	tc.ID = id
	tc.Function.Name = name
	tc.Function.Arguments = args
	if n := len(out); n > 0 && out[n-1].Role == "assistant" {
		out[n-1].ToolCalls = append(out[n-1].ToolCalls, tc)
		return out
	}
	return append(out, message{Role: "assistant", ToolCalls: []toolCall{tc}})
}

// heard is a speak call's result: whether the person heard it all, was cut
// off after the words in the call, or is still hearing it.
func heard(e journal.Entry) string {
	switch {
	case e.Cut:
		return `{"interrupted":true,"note":"the person cut you off and heard only the text in this call"}`
	case e.Pending:
		return `{"playing":true}`
	default:
		return `{"heard":true}`
	}
}

// result is what a call came back with. A success is the tool's own JSON;
// anything else says how it ended first, so a timeout is not mistaken for
// an answer (SPEC §7).
func result(e journal.Entry) string {
	if e.Outcome == "ok" {
		if e.Result == "" {
			return "{}"
		}
		return e.Result
	}
	r := struct {
		Outcome string          `json:"outcome"`
		Result  json.RawMessage `json:"result,omitempty"`
	}{Outcome: e.Outcome}
	if json.Valid([]byte(e.Result)) {
		r.Result = json.RawMessage(e.Result)
	}
	b, _ := json.Marshal(r)
	return string(b)
}
