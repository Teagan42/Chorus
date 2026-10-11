package journal

import (
	"encoding/json"
	"strings"
)

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

	// CutBy is why a cut entry stopped, as speech_truncated recorded it:
	// barge_in for the person, or the voice failing (ADR-0051). Empty in
	// logs from before it was recorded, which only a barge-in or a preempt
	// wrote.
	CutBy string

	// Canned marks a line the orchestrator said because the model or the
	// voice failed: words nobody's turn chose (SPEC §7, ADR-0051).
	Canned bool

	// Pending marks said speech still playing: Text is what the model asked
	// to say, and becomes what was heard once playback is recorded.
	Pending bool

	// Held marks pending speech an interjection paused: Text is what was
	// heard before it, and what plays after it is added (ADR-0056).
	Held bool

	// Acknowledges is the slow call whose acknowledgement this speech was:
	// words the model wrote as an argument, not a speak call (ADR-0039).
	Acknowledges string

	// Announces is the announcement this speech said: words nobody in the
	// conversation asked for, said because a timer went off or someone
	// elsewhere asked (SPEC §4). Nil for anything else.
	Announces *Announcement

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
		Canned       bool   `json:"canned"`
	}
	// A streamed speak's args carry no text (internal/session/session.go),
	// and an unreadable one is the model's mistake, not the log's: either way
	// there is nothing to say until playback says what was heard.
	_ = json.Unmarshal([]byte(args), &a)
	return appendEntry(s.Dialogue, Entry{
		Kind: EntrySaid, CallID: id, Text: a.Text, Pending: true,
		Acknowledges: a.Acknowledges, Announces: s.announcement(id), Canned: a.Canned,
	})
}

// cutBy records why a cut speak call stopped, once playback has settled it.
func (s State) cutBy(id, reason string) []Entry {
	i := s.said(id)
	if i < 0 {
		// A legacy event with no call id was appended last by played.
		i = len(s.Dialogue) - 1
	}
	out := cloneEntries(s.Dialogue)
	out[i].CutBy = reason
	return out
}

// played settles what a speak call was heard to say. A log written before
// speech events named their call gets the words where they were heard. A
// call that already played is one an interjection paused, resuming.
func (s State) played(id, text string, cut bool) []Entry {
	if i := s.said(id); i >= 0 {
		out := cloneEntries(s.Dialogue)
		if out[i].Held || !out[i].Pending {
			text = out[i].Text + text
		}
		out[i].Text, out[i].Cut, out[i].Pending, out[i].Held = text, cut, false, false
		return out
	}
	return appendEntry(s.Dialogue, Entry{Kind: EntrySaid, CallID: id, Text: text, Cut: cut})
}

// held records what a speak call was heard to say before an interjection
// paused it. It is still playing: the rest resumes after (ADR-0056).
func (s State) held(id, text string) []Entry {
	out := s.played(id, text, false)
	if i := (State{Dialogue: out}).said(id); i >= 0 {
		out[i].Pending, out[i].Held = true, true
	}
	return out
}

// dropped cuts a paused call whose rest was discarded before it resumed:
// the person heard the first half, and whatever emptied the queue cut it.
func (s State) dropped(id, reason string) []Entry {
	i := s.said(id)
	if i < 0 || !s.Dialogue[i].Held {
		return s.Dialogue
	}
	out := cloneEntries(s.Dialogue)
	out[i].Cut, out[i].CutBy, out[i].Pending, out[i].Held = true, reason, false, false
	return out
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
	if s.Dialogue[i].Held {
		// Its first half was heard, whatever became of the rest.
		out := cloneEntries(s.Dialogue)
		out[i].Pending, out[i].Held = false, false
		return out
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

// LastSaid is what the person last heard the assistant say: the heard words
// of the latest turn that said anything, cut ones included. Empty when
// nothing has been heard yet.
func (s State) LastSaid() string {
	var said []string
	for i := len(s.Dialogue) - 1; i >= 0; i-- {
		e := s.Dialogue[i]
		if e.Kind == EntryHeard && len(said) > 0 {
			break
		}
		if e.Kind == EntrySaid && !e.Pending && e.Text != "" {
			said = append([]string{e.Text}, said...)
		}
	}
	return strings.Join(said, " ")
}
