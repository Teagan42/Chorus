package curation

import (
	"fmt"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Rerun is one re-run a reviewer asked on Replay, kept whether or not any of
// it was promoted, so a take can be promoted after the page is gone
// (ADR-0059). A new run is a new row; none is ever replaced.
type Rerun struct {
	// ID is the store's, given when the re-run is added.
	ID             uint64
	ConversationID string

	// What it ran under, as a Promotion keeps it: the versions, and the
	// edited prompt and tool declarations themselves.
	Versions     journal.Versions
	SystemPrompt string
	ToolSchema   string

	// Takes are the turns that ran, in order. A run the model stopped keeps
	// the turns before it.
	Takes []Take

	RanAt time.Time
}

// Take is what a re-run did with one turn, named by its utterance seq.
type Take struct {
	Seq    uint64 `json:"seq"`
	Speech string `json:"speech"`
	Calls  []Call `json:"calls"`
	Finish string `json:"finish"`
}

// Turn returns the take of the turn seq, if the run reached it.
func (r Rerun) Turn(seq uint64) (Take, bool) {
	for _, t := range r.Takes {
		if t.Seq == seq {
			return t, true
		}
	}
	return Take{}, false
}

func (r Rerun) validate() error {
	if r.ConversationID == "" {
		return fmt.Errorf("curation: re-run without a conversation")
	}
	// A take promoted from it must say what it ran under.
	if v := r.Versions; v.Model == "" || v.Prompt == "" || v.ToolSchema == "" {
		return fmt.Errorf("curation: re-run of %s with versions %+v", r.ConversationID, r.Versions)
	}
	if len(r.Takes) == 0 {
		return fmt.Errorf("curation: re-run of %s ran no turn", r.ConversationID)
	}
	for _, t := range r.Takes {
		if t.Seq == 0 {
			return fmt.Errorf("curation: re-run of %s with a take of no turn", r.ConversationID)
		}
	}
	return nil
}

// cloneTakes copies the takes, reading an empty call list as none, as
// Postgres would.
func cloneTakes(ts []Take) []Take {
	out := make([]Take, len(ts))
	for i, t := range ts {
		if len(t.Calls) == 0 {
			t.Calls = nil
		} else {
			t.Calls = append([]Call(nil), t.Calls...)
		}
		out[i] = t
	}
	return out
}
