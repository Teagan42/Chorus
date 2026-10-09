//go:build db

package session_test

import (
	"context"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/memory"
	"github.com/teaganglenn/chorus/internal/session"
)

// Teagan tells the kitchen about the oat milk with the journal and memory in
// Postgres, as chorusd runs them. After a restart the next conversation is
// told, and a replay of that conversation from the database recalls the oat
// milk even once Teagan has asked to forget it.
//
// verifies SPEC §5, §8
func TestWhatTeaganAskedToBeRememberedSurvivesARestartInPostgres(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	if err := memory.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	// The whole table: what another package's suite shared would be recalled
	// to Teagan too. `task test:db` runs packages one at a time.
	if _, err := pool.Exec(ctx, `TRUNCATE memories`); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	pg := journal.NewPgStore(pool)
	mems := memory.NewPgStore(pool)

	daemon := func(m *model) *rig {
		return newRigWith(t, nil, nil, nil, func(cfg *session.Config) {
			cfg.Engine = m
			cfg.Store = pg
			cfg.Journal = journal.New(pg, cfg.Clock, versions())
			cfg.Tools = memory.Tools(mems, cfg.Clock)
			cfg.Memories = memory.Recaller(mems)
		})
	}

	yesterday := daemon(&model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"Takes oat milk in coffee."}`}, done}
		}
		return []session.Action{done}
	}})
	s := yesterday.open(t, "teagan")
	wait(t, heard(s, "remember that I take oat milk in my coffee"))
	st, err := journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c := callByID(t, st, "call_r1"); c.Outcome != "ok" {
		t.Fatalf("remember = %+v", c)
	}

	var told []journal.Memory
	today := daemon(&model{answer: func(in session.Input) []session.Action {
		told = in.Memories
		return []session.Action{done}
	}})
	s = today.open(t, "teagan")
	wait(t, heard(s, "how do I take my coffee"))
	if len(told) != 1 || told[0].Fact != "Takes oat milk in coffee." || told[0].Person != "teagan" {
		t.Fatalf("this morning's ask was told %+v, want the oat milk", told)
	}

	if gone, err := mems.Forget(ctx, "teagan", told[0].ID); err != nil || !gone {
		t.Fatalf("forget: gone=%v err=%v", gone, err)
	}
	st, err = journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(st.Recalled) != 1 || st.Recalled[0] != told[0] {
		t.Errorf("replay recalled %+v, want what the turn was told: %+v", st.Recalled, told)
	}
}
