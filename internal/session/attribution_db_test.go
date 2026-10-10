//go:build db

package session_test

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/session"
)

// Teagan asks the kitchen how she takes her coffee with the journal and
// memory in Postgres, as chorusd runs them, and a friend over for brunch
// asks to have it forgotten. The friend's voice matched nobody: the forget
// is refused, the oat milk is still in the database, and a replay of the
// conversation from Postgres ends with a guest who was told nothing, the
// empty person on the guest's recall surviving the round trip.
//
// verifies SPEC §5, §8
func TestAGuestCannotForgetTeagansOatMilkInPostgres(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	if err := memory.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE memories, conversation_summaries`); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	pg := journal.NewPgStore(pool)
	mems := memory.NewPgStore(pool)
	oat := memory.Memory{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee.", At: epoch}
	if err := mems.Remember(ctx, oat); err != nil {
		t.Fatalf("remember: %v", err)
	}

	m := &model{answer: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		if last.Kind == journal.EntryHeard && last.Text == "forget the oat milk thing" {
			return []session.Action{session.ToolCall{ID: "call_f1", Tool: "forget", Args: `{"memory_id":"m_3f9c2a10"}`}, done}
		}
		return []session.Action{done}
	}}
	r := newRigWith(t, nil, nil, nil, func(cfg *session.Config) {
		cfg.Engine = m
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
		cfg.Tools = memory.Tools(mems, cfg.Clock)
		cfg.Memories = memory.Recaller(mems, memory.RecallConfig{})
	})
	s := r.open(t, "teagan")
	wait(t, voiced(s, "how do i take my coffee", "teagan", "identified"))
	wait(t, voiced(s, "forget the oat milk thing", "", "below_threshold"))

	st, err := journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c := callByID(t, st, "call_f1"); c.Outcome != "error" || c.Result != `{"error":"unidentified_speaker"}` {
		t.Errorf("forget = %+v, want refused as unidentified", c)
	}
	if st.Speaker != "" || len(st.Recalled) != 0 || st.RecalledFor != "" {
		t.Errorf("replayed as %q told %+v for %q, want a guest told nothing", st.Speaker, st.Recalled, st.RecalledFor)
	}
	if got, err := mems.Recall(ctx, "teagan", memory.RecallLimit); err != nil || len(got) != 1 || got[0].ID != oat.ID {
		t.Errorf("Teagan remembers %+v (err %v), want the oat milk kept", got, err)
	}
}
