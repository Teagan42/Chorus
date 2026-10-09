//go:build db

package session_test

import (
	"context"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// Alan's film search with the journal in Postgres: the acknowledgement is a
// speak call of its own in the log the database holds, tied to the search,
// and a replay tells the dialogue the same way.
//
// verifies SPEC §4.4, §8
func TestTheAcknowledgementIsInThePostgresLog(t *testing.T) {
	pg := journal.NewPgStore(openPg(t))
	search := newSearch()
	close(search.release)
	m := &model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{
				ID: "call_c1", Tool: "media_search",
				Args: `{"query":"adventure films starring Tom Holland","acknowledgement":"Let me look through the library."}`,
			}, done}
		}
		return []session.Action{done}
	}}
	r := newRigWith(t, nil, map[string]session.Tool{"media_search": search}, nil, func(cfg *session.Config) {
		cfg.Engine = m
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
	})

	s := r.open(t, "alan")
	wait(t, heard(s, "recommend a movie like indiana jones starring tom holland"))

	st, err := journal.Replay(context.Background(), pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	var ack journal.Entry
	for _, e := range st.Dialogue {
		if e.CallID == "call_c1_ack" {
			ack = e
		}
	}
	if ack.Text != "Let me look through the library." || ack.Acknowledges != "call_c1" || ack.Pending {
		t.Errorf("acknowledgement replayed as %+v, want it heard and tied to call_c1", ack)
	}
}
