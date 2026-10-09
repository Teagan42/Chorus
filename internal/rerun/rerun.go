// Package rerun is edit-and-replay (SPEC §9.2): it reads each turn the
// journal recorded, asks a turn engine the same question again, perhaps under
// a different prompt or model, and says what changed.
//
// It is safe to point at the live model. The engine is single-shot: a turn is
// the system prompt and one transcript, and tool results never feed back into
// it. So a re-run executes nothing; it only compares the calls the model
// would make with the ones it made.
package rerun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
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
}

// Turns splits a conversation's log at each transcribed utterance. The
// speaker is the one the reducer held when the turn ran, which is what the
// session handed the engine.
func Turns(events []journal.Event) ([]Turn, error) {
	var (
		out   []Turn
		state journal.State
		err   error
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
			out = append(out, Turn{Seq: e.Seq, Speaker: state.Speaker, Text: f["text"], Versions: e.Versions})
			continue
		}
		if len(out) == 0 {
			continue
		}
		t := &out[len(out)-1]
		switch e.Kind {
		case journal.KindSpeechSpoken:
			t.Recorded.Speech = append(t.Recorded.Speech, Speech{Text: f["text"]})
		case journal.KindSpeechTruncated:
			t.Recorded.Speech = append(t.Recorded.Speech, Speech{Text: f["spoken_text"], Unheard: f["unspoken_text"]})
		case journal.KindSpeechDiscarded:
			t.Recorded.Speech = append(t.Recorded.Speech, Speech{Unheard: f["unspoken_text"]})
		case journal.KindToolCalled:
			if f["tool"] != toolSpeak {
				t.Recorded.Calls = append(t.Recorded.Calls, Call{Tool: f["tool"], Args: f["args_json"]})
			}
		case journal.KindModelCompleted:
			t.Recorded.Finish = f["finish_reason"]
			t.Versions = e.Versions
		}
	}
	return out, nil
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
	actions, err := eng.Turn(ctx, session.Input{ConversationID: conversationID, Speaker: t.Speaker, Text: t.Text})
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
