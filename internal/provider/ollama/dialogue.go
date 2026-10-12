package ollama

import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
)

// dialogue renders the conversation the session derived from the log as
// /api/chat messages, in order (SPEC §4.4). What is said is chat.Dialogue's;
// this frames it: arguments as a native JSON object, and each result named
// by its tool, which is how this endpoint matches a result to its call.
func dialogue(entries []journal.Entry) []message {
	steps := chat.Dialogue(entries)
	out := make([]message, 0, len(steps))
	for _, s := range steps {
		m := message{Role: s.Role, Content: s.Content, ToolName: s.Tool}
		for _, c := range s.Calls {
			var tc toolCall
			tc.ID = c.ID
			tc.Function.Name = c.Name
			tc.Function.Arguments = c.Args
			m.ToolCalls = append(m.ToolCalls, tc)
		}
		out = append(out, m)
	}
	return out
}

// heard is a speak call's result: whether the person heard it all, was cut
// off after the words in the call, or is still hearing it.
func heard(e journal.Entry) string { return chat.Heard(e) }

// result is what a call came back with, its outcome first unless it is ok.
func result(e journal.Entry) string { return chat.Result(e) }
