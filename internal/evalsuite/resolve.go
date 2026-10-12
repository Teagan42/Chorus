package evalsuite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/session"
)

// Resolved is a case with its turn read back from the log.
type Resolved struct {
	Case Case

	// Turn is the turn as internal/rerun reads it, and Input what the case
	// is asked: what Replay asks, with the dialogue the live session's first
	// ask was sent (ADR-0079).
	Turn  rerun.Turn
	Input session.Input

	// Recorded is the context the turn ran under, which the baseline asks.
	Recorded Context

	Evidence Evidence

	// Skipped is why the case cannot be asked as its turn was, such as
	// "context_missing: tool_schema". A skipped case scores nothing.
	Skipped string
}

// Context is a prompt and tool set, by their labels and their text.
type Context struct {
	Versions   journal.Versions
	Prompt     string
	ToolSchema string
	Tools      map[string]registry.ToolSpec
}

// Resolve reads each case's turn from store, and the context it ran under
// from src. A ref the log does not hold is refused, every one at once,
// each with what the log holds instead.
func Resolve(ctx context.Context, store journal.Store, src contexts.Source, s Suite) ([]Resolved, error) {
	logs := map[string][]journal.Event{}
	var (
		out  []Resolved
		errs []error
	)
	for _, c := range s.Cases {
		ref := c.Turn()
		events, ok := logs[ref.Conversation]
		if !ok {
			var err error
			if events, err = store.Events(ctx, ref.Conversation); err != nil {
				return nil, fmt.Errorf("read %s: %w", ref.Conversation, err)
			}
			logs[ref.Conversation] = events
		}
		r, err := resolve(c, events, src)
		if err != nil {
			errs = append(errs, fmt.Errorf("case %s: %w", c.ID, err))
			continue
		}
		out = append(out, r)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return out, nil
}

func resolve(c Case, events []journal.Event, src contexts.Source) (Resolved, error) {
	ref := c.Turn()
	if len(events) == 0 {
		return Resolved{}, fmt.Errorf("%s has no log: no conversation has that id, or retention deleted it", ref.Conversation)
	}
	turns, err := rerun.Turns(events)
	if err != nil {
		return Resolved{}, fmt.Errorf("%s does not read back: %w", ref.Conversation, err)
	}
	i := -1
	for j, t := range turns {
		if t.Seq == ref.Seq {
			i = j
		}
	}
	if i < 0 {
		return Resolved{}, notATurn(ref, events, turns)
	}
	t := turns[i]
	in := rerun.Input(ref.Conversation, t)
	in.Dialogue = t.Dialogue
	r := Resolved{Case: c, Turn: t, Input: in, Evidence: evidence(events, t.Seq, next(turns, i))}
	r.Recorded, r.Skipped = recorded(t.Versions, src)
	return r, nil
}

// next is the seq of the utterance after turn i, or zero for the last.
func next(turns []rerun.Turn, i int) uint64 {
	if i+1 < len(turns) {
		return turns[i+1].Seq
	}
	return 0
}

// notATurn says what the log holds where a ref points, and which seqs
// would have been turns.
func notATurn(ref Ref, events []journal.Event, turns []rerun.Turn) error {
	var listed []string
	for _, t := range turns {
		listed = append(listed, fmt.Sprintf("#%d %q", t.Seq, t.Text))
	}
	holds := "turns: none"
	if len(listed) > 0 {
		holds = "its turns are " + strings.Join(listed, ", ")
	}
	for _, e := range events {
		if e.Seq == ref.Seq {
			what := string(e.Kind)
			if e.Speculative {
				what = "speculative " + what
			}
			return fmt.Errorf("%s is %s, not an utterance; %s", ref, what, holds)
		}
	}
	return fmt.Errorf("%s has no event #%d; %s", ref.Conversation, ref.Seq, holds)
}

// recorded resolves the labels a turn ran under. A label src does not hold
// is never guessed at (ADR-0071): the case is skipped, naming what is gone.
func recorded(v journal.Versions, src contexts.Source) (Context, string) {
	c := Context{Versions: v}
	var missing []string
	prompt, ok := src.Prompt(v.Prompt)
	if ok {
		c.Prompt = prompt
	} else {
		missing = append(missing, "prompt")
	}
	schema, ok := src.ToolSchema(v.ToolSchema)
	if ok {
		tools, err := ollama.ParseToolSchema(schema)
		if err != nil {
			return c, fmt.Sprintf("context_unreadable: tool_schema %s: %v", v.ToolSchema, err)
		}
		c.ToolSchema, c.Tools = schema, tools
	} else {
		missing = append(missing, "tool_schema")
	}
	if len(missing) > 0 {
		return c, "context_missing: " + strings.Join(missing, ", ")
	}
	return c, ""
}

// evidence is what the conversation showed the turn at seq, which ends
// before the utterance at end, or with the log when end is zero: the
// entities its results named and its calls named that did not fail, and
// the confirmations handed out before it.
func evidence(events []journal.Event, seq, end uint64) Evidence {
	ev := Evidence{Entities: map[string]bool{}, Nonces: map[string]bool{}}
	args := map[string]string{}
	for _, e := range events {
		if end != 0 && e.Seq >= end {
			break
		}
		if e.Speculative {
			continue
		}
		f := e.Fields
		switch e.Kind {
		case journal.KindToolCalled:
			args[f["call_id"]] = f["args_json"]
		case journal.KindToolResult:
			for _, id := range entityIn.FindAllString(f["result_json"], -1) {
				ev.Entities[id] = true
			}
			// A call the house refused may have named what it lacks.
			if f["outcome"] != "error" {
				for _, id := range entitiesIn(args[f["call_id"]]) {
					ev.Entities[id] = true
				}
			}
		case journal.KindConfirmationRequested:
			if e.Seq < seq {
				ev.Nonces[f["nonce"]] = true
			}
		}
	}
	return ev
}
