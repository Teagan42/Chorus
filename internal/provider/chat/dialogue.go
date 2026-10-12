package chat

import (
	"encoding/json"

	"github.com/teagan42/chorus/internal/journal"
)

// Step is one chat message of the dialogue, before an engine frames it for
// its endpoint: a user's words, an assistant's calls, or one call's result.
type Step struct {
	// Role is user, assistant or tool.
	Role string
	// Content is what a user said, or what a tool returned.
	Content string
	// Calls are an assistant step's calls, in the order the model made them.
	Calls []Call
	// Tool and CallID name the call a tool step answers: Ollama matches a
	// result by the tool's name, an OpenAI-shaped endpoint by the call's id.
	Tool   string
	CallID string
}

// Call is one call the model made, with its arguments as valid JSON.
type Call struct {
	ID   string
	Name string
	Args json.RawMessage
}

// Dialogue renders the conversation the session derived from the log as
// chat steps, in order (SPEC §4.4).
//
// Speech goes back as the speak calls the model made, never as assistant
// content: the prompt tells the model content is never heard, and a history
// that answers in content teaches it otherwise. Each speak carries the words
// the person heard, and its result says whether they heard all of them.
func Dialogue(entries []journal.Entry) []Step {
	var out []Step
	for _, e := range entries {
		switch e.Kind {
		case journal.EntryHeard:
			out = append(out, Step{Role: "user", Content: e.Text})
		case journal.EntrySaid:
			if e.Text == "" {
				// Streamed speech still playing whose words are not known
				// yet: nothing to tell the model it said.
				continue
			}
			if e.Canned {
				// The orchestrator's apology for a failure, not the model's
				// words: shown it, the model learns to say it unprompted. The
				// failure itself is in the speak result it follows (ADR-0051).
				continue
			}
			if e.Acknowledges != "" && !e.Cut {
				// The model wrote these words as the slow call's argument,
				// which it is shown already. A speak call it never made
				// would teach it to make one, and say everything twice.
				continue
			}
			// The words alone: the mode the model chose is the speech
			// channel's business, and an empty one is not in the enum.
			args, _ := json.Marshal(map[string]string{"text": e.Text})
			out = calling(out, e.CallID, Speak, args)
			out = append(out, Step{Role: "tool", Tool: Speak, CallID: e.CallID, Content: Heard(e)})
		case journal.EntryCall:
			args := json.RawMessage(e.Args)
			if !json.Valid(args) {
				// Recorded verbatim from a model that wrote broken JSON. The
				// result says the call failed; resending it would fail the ask.
				args = json.RawMessage("{}")
			}
			out = calling(out, e.CallID, e.Tool, args)
		case journal.EntryResult:
			out = append(out, Step{Role: "tool", Tool: e.Tool, CallID: e.CallID, Content: Result(e)})
		}
	}
	return out
}

// calling adds a call to the assistant step that is still open, or opens
// one: calls the model made together go back together.
func calling(out []Step, id, name string, args json.RawMessage) []Step {
	c := Call{ID: id, Name: name, Args: args}
	if n := len(out); n > 0 && out[n-1].Role == "assistant" {
		out[n-1].Calls = append(out[n-1].Calls, c)
		return out
	}
	return append(out, Step{Role: "assistant", Calls: []Call{c}})
}

// Heard is a speak call's result: whether the person heard it all, was cut
// off after the words in the call, or is still hearing it.
func Heard(e journal.Entry) string {
	var r string
	switch {
	case e.Cut && voiceFailed(e.CutBy):
		r = `{"interrupted":true,"note":"your voice failed partway: the person heard only the text in this call, and did not cut you off"}`
	case e.Cut:
		r = `{"interrupted":true,"note":"the person cut you off and heard only the text in this call"}`
	case e.Pending:
		r = `{"playing":true}`
	default:
		r = `{"heard":true}`
	}
	if e.Announces == nil {
		return r
	}
	return r[:len(r)-1] + `,"announcement":` + announcement(e.Announces) + "}"
}

// voiceFailed reports a cut the voice or the device made, not the person.
func voiceFailed(cutBy string) bool {
	return cutBy == "tts_unavailable" || cutBy == "playback_unconfirmed"
}

// announcement says why something nobody here asked for was said, and
// whether an answer was asked for. Without it, "yes please" answers no
// question (SPEC §4).
func announcement(a *journal.Announcement) string {
	why := struct {
		Source            string `json:"source"`
		TimerID           string `json:"timer_id,omitempty"`
		RequestedBy       string `json:"requested_by,omitempty"`
		From              string `json:"from,omitempty"`
		StartConversation bool   `json:"answer_expected,omitempty"`
	}{a.Source, a.TimerID, a.RequestedBy, a.FromSatellite, a.StartConversation}
	// Strings and a bool: marshalling cannot fail.
	b, _ := json.Marshal(why)
	return string(b)
}

// Result is what a call came back with. A success is the tool's own JSON;
// anything else says how it ended first, so a timeout is not mistaken for
// an answer (SPEC §7).
func Result(e journal.Entry) string {
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
