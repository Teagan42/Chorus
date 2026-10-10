// Package rerun is edit-and-replay (SPEC §9.2): it reads each turn the
// journal recorded, asks a turn engine the same question again, perhaps under
// a different prompt or model, and says what changed.
//
// It is safe to point at the live model. An engine never runs a tool: asking
// again with results is the session's job (ADR-0037), and a re-run asks once,
// with the system prompt, what the turn remembered, and the transcript. So a
// re-run executes nothing; it only compares the calls the model would make
// with the ones it made, and the take it compares against is the turn's
// first ask.
package rerun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// toolSpeak is how the model talks; the session records it as a call so the
// journal has one representation of speech (ADR-0003). It is not an action.
const toolSpeak = "speak"

// Speech is one utterance the model generated: Text is what was said aloud,
// Unheard the part a barge-in cut or discarded. A replayed take has no
// playback, so all of it is Text.
type Speech struct {
	Text    string
	Unheard string
}

// Call is one tool call the model made, other than speaking.
type Call struct {
	Tool string
	Args string
}

// Take is what the model did with one turn.
type Take struct {
	Speech []Speech
	Calls  []Call
	Finish string
}

// Said is everything the take generated, heard or not, as one line.
func (t Take) Said() string {
	parts := make([]string, 0, len(t.Speech))
	for _, s := range t.Speech {
		parts = append(parts, s.Text+s.Unheard)
	}
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

// Turn is one utterance and the take the journal recorded for it.
type Turn struct {
	Seq      uint64
	Speaker  string
	Text     string
	Versions journal.Versions
	Recorded Take

	// Memories are what the turn was told it remembers: the memory_recalled
	// it recorded before its first ask, or the last one before it, since an
	// unchanged memory is not recorded again (SPEC §5).
	Memories []journal.Memory

	// Summaries are the speaker's earlier conversations the turn was told
	// of, from the same memory_recalled, and HeardAt the time it was told it
	// was: when the utterance was logged.
	Summaries []journal.Summary
	HeardAt   time.Time

	// Room is where the satellite that heard it stands, from the log.
	Room string
}

// Turns splits a conversation's log at each transcribed utterance. The
// speaker is the one the reducer held when the turn ran, which is what the
// session handed the engine.
//
// A turn's recorded take is its first ask: the calls made before its first
// completion, and the speech of those calls. A follow-up ask answered tool
// results a re-run never has, so comparing against it would report every
// answer as a change.
func Turns(events []journal.Event) ([]Turn, error) {
	var (
		out   []Turn
		state journal.State
		err   error
		// asked is set once the turn's first ask completed; later holds the
		// follow-up asks' speak calls, whose speech is not the first ask's.
		asked bool
		later map[string]bool
		// held is each call an interjection paused, by its take's index:
		// what plays after is the same line (ADR-0056).
		held map[string]int
	)
	for _, e := range events {
		if state, err = journal.Reduce(state, e); err != nil {
			return nil, fmt.Errorf("turns seq %d: %w", e.Seq, err)
		}
		if e.Speculative {
			continue
		}
		f := e.Fields
		if e.Kind == journal.KindUtteranceTranscribed {
			out = append(out, Turn{
				Seq: e.Seq, Speaker: state.Speaker, Text: f["text"], Versions: e.Versions,
				Memories: state.Recalled, Summaries: state.RecalledSummaries, HeardAt: state.HeardAt,
				Room: state.Room,
			})
			asked, later, held = false, map[string]bool{}, map[string]int{}
			continue
		}
		if len(out) == 0 {
			continue
		}
		t := &out[len(out)-1]
		switch e.Kind {
		case journal.KindAnnouncementMade:
			// Said for a timer or another room, not by the turn's model.
			later[f["call_id"]] = true
		case journal.KindModelFailed, journal.KindSpeechFailed:
			// The canned line is the orchestrator's, not the model's (ADR-0051).
			if id := f["canned_call_id"]; id != "" {
				later[id] = true
			}
		case journal.KindMemoryRecalled:
			if !asked {
				t.Memories, t.Summaries = state.Recalled, state.RecalledSummaries
			}
		case journal.KindSpeechSpoken:
			if !later[f["call_id"]] {
				t.said(held, f["call_id"], Speech{Text: f["text"]})
			}
		case journal.KindSpeechTruncated:
			switch {
			case later[f["call_id"]]:
			case f["reason"] == "interjected":
				t.said(held, f["call_id"], Speech{Text: f["spoken_text"]})
				held[f["call_id"]] = len(t.Recorded.Speech) - 1
			default:
				t.said(held, f["call_id"], Speech{Text: f["spoken_text"], Unheard: f["unspoken_text"]})
			}
		case journal.KindSpeechDiscarded:
			// A log from before ADR-0051 names no call, so its discarded
			// speech is kept with the turn whichever ask generated it.
			if !later[f["call_id"]] {
				t.said(held, f["call_id"], Speech{Unheard: f["unspoken_text"]})
			}
		case journal.KindToolCalled:
			switch {
			case asked && f["tool"] == toolSpeak:
				later[f["call_id"]] = true
			case asked:
			case f["tool"] == toolSpeak && acknowledges(f["args_json"]):
				// The session's speak for a slow call: the model wrote the
				// words as that call's argument, which a re-run of the call
				// carries too, and never speaks them itself (ADR-0039).
				later[f["call_id"]] = true
			case f["tool"] != toolSpeak:
				t.Recorded.Calls = append(t.Recorded.Calls, Call{Tool: f["tool"], Args: f["args_json"]})
			}
		case journal.KindModelCompleted:
			if !asked {
				t.Recorded.Finish = f["finish_reason"]
				t.Versions = e.Versions
			}
			asked = true
		}
	}
	return out, nil
}

// acknowledges reports a speak call the session made to say a slow call's
// acknowledgement. A streamed speak's arguments carry no such field.
func acknowledges(args string) bool {
	var a struct {
		Acknowledges string `json:"acknowledges"`
	}
	// An unreadable speak is the model's, so it is the turn's speech.
	_ = json.Unmarshal([]byte(args), &a)
	return a.Acknowledges != ""
}

// said adds speech to the take, or to the line an interjection paused,
// which it then no longer is.
func (t *Turn) said(held map[string]int, id string, sp Speech) {
	i, ok := held[id]
	if !ok || id == "" {
		t.Recorded.Speech = append(t.Recorded.Speech, sp)
		return
	}
	delete(held, id)
	t.Recorded.Speech[i].Text += sp.Text
	t.Recorded.Speech[i].Unheard = sp.Unheard
}

// finishError is how an engine ends a turn its stream broke under: the
// Ollama decoder reports a mid-stream failure on the channel, since Turn's
// own error is spent before the first chunk (internal/provider/ollama).
const finishError = "error"

// Failed is a turn the model started answering and could not finish. What
// it produced before failing is a fragment, not an answer to compare.
type Failed struct{ Reason string }

func (f *Failed) Error() string { return "the model failed mid-answer: " + f.Reason }

// Run asks eng the turn's question again and collects what it does. It
// drains the stream to the end, as the engine contract requires, and
// returns a *Failed alongside the partial take when the turn ended in error.
func Run(ctx context.Context, eng session.Engine, conversationID string, t Turn) (Take, error) {
	actions, err := eng.Turn(ctx, session.Input{
		ConversationID: conversationID, Speaker: t.Speaker, Room: t.Room, Text: t.Text,
		Memories: t.Memories, Summaries: t.Summaries, Now: t.HeardAt,
	})
	if err != nil {
		return Take{}, err
	}
	var (
		take       Take
		completion string
		index      = map[string]int{}
	)
	for a := range actions {
		switch act := a.(type) {
		case session.SpeechDelta:
			i, ok := index[act.CallID]
			if !ok {
				i = len(take.Speech)
				index[act.CallID] = i
				take.Speech = append(take.Speech, Speech{})
			}
			take.Speech[i].Text += act.Text
		case session.ToolCall:
			take.Calls = append(take.Calls, Call{Tool: act.Tool, Args: act.Args})
		case session.TurnEnd:
			take.Finish = act.FinishReason
			completion = act.Completion
		}
	}
	if take.Finish == finishError {
		return take, &Failed{Reason: failure(completion)}
	}
	return take, nil
}

// failure is why a completion ended in error: its error field when the
// engine wrote one, else the completion as recorded.
func failure(completion string) string {
	var c struct {
		Error string `json:"error"`
	}
	if json.Unmarshal([]byte(completion), &c) == nil && c.Error != "" {
		return c.Error
	}
	if completion != "" {
		return completion
	}
	return "finish_reason " + finishError
}

// Change says which side of a take differs.
type Change struct {
	Speech bool
	Calls  bool
}

// Compare compares two takes. Speech is compared as words, so a delta
// boundary is not a change; calls by tool and arguments, so key order is
// not either.
func Compare(recorded, replayed Take) Change {
	c := Change{Speech: recorded.Said() != replayed.Said()}
	if len(recorded.Calls) != len(replayed.Calls) {
		c.Calls = true
		return c
	}
	for i := range recorded.Calls {
		a, b := recorded.Calls[i], replayed.Calls[i]
		if a.Tool != b.Tool || canonical(a.Args) != canonical(b.Args) {
			c.Calls = true
		}
	}
	return c
}

// canonical re-encodes JSON arguments with sorted keys. Arguments that are
// not JSON compare as written.
func canonical(args string) string {
	var v any
	if err := json.Unmarshal([]byte(args), &v); err != nil {
		return args
	}
	b, err := json.Marshal(v)
	if err != nil {
		return args
	}
	return string(b)
}
