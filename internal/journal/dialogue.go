package journal

import "encoding/json"

// EntryKind is what one step of the dialogue was.
type EntryKind string

const (
	// EntryHeard is the person speaking: a final transcript.
	EntryHeard EntryKind = "heard"
	// EntrySaid is the assistant speaking, as the person heard it.
	EntrySaid EntryKind = "said"
	// EntryCall is the assistant calling a tool other than speak.
	EntryCall EntryKind = "call"
	// EntryResult is what a tool call came back with.
	EntryResult EntryKind = "result"
)

// Entry is one step of the conversation as the model is told it, in the
// order it happened (SPEC §4.4). Speech is what the person heard, not what
// the model generated: a cut utterance carries the heard half only, and
// speech nobody heard is not here at all.
type Entry struct {
	Kind   EntryKind
	CallID string

	// Text is the transcript of a heard entry, or the heard words of a said one.
	Text string

	// Speaker is who a heard entry was attributed to: the speaker it matched,
	// or the conversation's current one when it matched nobody, and empty in
	// a guest's conversation. Whom a summary says asked what (SPEC §5).
	Speaker string

	// Cut marks said speech a barge-in stopped after Text.
	Cut bool

	// Pending marks said speech still playing: Text is what the model asked
	// to say, and becomes what was heard once playback is recorded.
	Pending bool

	// Acknowledges is the slow call whose acknowledgement this speech was:
	// words the model wrote as an argument, not a speak call (ADR-0039).
	Acknowledges string

	// Tool and Args are a call's; Tool, Outcome and Result are a result's.
	Tool    string
	Args    string
	Outcome string
	Result  string
}

// toolSpeak is the one tool that is speech rather than an action (ADR-0003).
const toolSpeak = "speak"

// called opens a dialogue step for a tool call. A speak call holds its place
// in the dialogue where the model made it, because that is where the model
// will expect to find it; its words arrive when playback is recorded.
func (s State) called(id, tool, args string) []Entry {
	if tool != toolSpeak {
		return appendEntry(s.Dialogue, Entry{Kind: EntryCall, CallID: id, Tool: tool, Args: args})
	}
	var a struct {
		Text         string `json:"text"`
		Acknowledges string `json:"acknowledges"`
	}
	// A streamed speak's args carry no text (internal/session/session.go),
	// and an unreadable one is the model's mistake, not the log's: either way
	// there is nothing to say until playback says what was heard.
	_ = json.Unmarshal([]byte(args), &a)
	return appendEntry(s.Dialogue, Entry{Kind: EntrySaid, CallID: id, Text: a.Text, Pending: true, Acknowledges: a.Acknowledges})
}

// played settles what a speak call was heard to say. A log written before
// speech events named their call gets the words where they were heard.
func (s State) played(id, text string, cut bool) []Entry {
	if i := s.said(id); i >= 0 {
		out := cloneEntries(s.Dialogue)
		out[i].Text, out[i].Cut, out[i].Pending = text, cut, false
		return out
	}
	return appendEntry(s.Dialogue, Entry{Kind: EntrySaid, CallID: id, Text: text, Cut: cut})
}

// resulted closes a call. A speak still pending at its result was never
// heard, because playback records what was heard before the result, so it
// leaves the dialogue: the model must not believe the person heard it
// (SPEC §4.4). Any other call's result becomes a step of its own.
func (s State) resulted(id, tool, outcome, result string) []Entry {
	if tool != toolSpeak {
		return appendEntry(s.Dialogue, Entry{Kind: EntryResult, CallID: id, Tool: tool, Outcome: outcome, Result: result})
	}
	i := s.said(id)
	if i < 0 || !s.Dialogue[i].Pending {
		return s.Dialogue
	}
	out := make([]Entry, 0, len(s.Dialogue)-1)
	out = append(out, s.Dialogue[:i]...)
	return append(out, s.Dialogue[i+1:]...)
}

// said finds a speak call's pending step. An empty id is a legacy speech
// event, which has no call to settle.
func (s State) said(id string) int {
	if id == "" {
		return -1
	}
	for i, e := range s.Dialogue {
		if e.Kind == EntrySaid && e.CallID == id {
			return i
		}
	}
	return -1
}

// appendEntry never writes into a backing array an earlier State shares.
func appendEntry(d []Entry, e Entry) []Entry {
	out := make([]Entry, len(d), len(d)+1)
	copy(out, d)
	return append(out, e)
}

func cloneEntries(d []Entry) []Entry {
	out := make([]Entry, len(d))
	copy(out, d)
	return out
}
