//go:build db

// Tagged db because `task test` is hermetic. Run with `task test:db`.
package session_test

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// openPg connects and migrates. An unreachable database skips unless the DSN
// was set explicitly, so CI cannot silently pass.
func openPg(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, journal.DSN())
	if err == nil {
		err = pool.Ping(ctx)
	}
	if err != nil {
		if os.Getenv(journal.DSNEnv) != "" {
			t.Fatalf("%s is set but unreachable: %v", journal.DSNEnv, err)
		}
		t.Skipf("no Postgres at the default DSN (%v); run `docker compose up -d postgres`", err)
	}
	t.Cleanup(pool.Close)
	if err := journal.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate journal: %v", err)
	}
	return pool
}

// Teagan's garage question with the journal in Postgres, as chorusd runs it:
// the follow-up ask is told the cover's state from the log the database
// holds, and a replay from that log tells the same dialogue.
//
// verifies SPEC §4.4, §8
func TestTheFollowUpIsToldTheDialogueFromPostgres(t *testing.T) {
	pg := journal.NewPgStore(openPg(t))
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Let me check.", Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, map[string]session.Tool{"ha_get_state": reads(garageState)}, nil, func(cfg *session.Config) {
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
	})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s2", Text: "No, the garage door is open.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}

	s := r.open(t, "teagan")
	wait(t, heard(s, "is the garage door closed"))

	asks := r.engine.asks()
	if len(asks) != 2 {
		t.Fatalf("model asked %d times, want 2", len(asks))
	}
	want := journal.Entry{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: garageState}
	if got := lastEntry(t, asks[1]); got != want {
		t.Errorf("follow-up ended on %+v, want %+v", got, want)
	}

	st, err := journal.Replay(context.Background(), pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	wantDialogue := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "is the garage door closed", Speaker: "teagan"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check."},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
		want,
		{Kind: journal.EntrySaid, CallID: "call_s2", Text: "No, the garage door is open."},
	}
	if !reflect.DeepEqual(st.Dialogue, wantDialogue) {
		t.Errorf("replayed dialogue =\n%+v\nwant\n%+v", st.Dialogue, wantDialogue)
	}
}
