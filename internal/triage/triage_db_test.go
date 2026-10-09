//go:build db

// Tagged db because `task test` is hermetic. Run with `task test:db`.
package triage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/triage"
)

// The repeat window is seconds measured between two rows' timestamps, so it
// has to survive the round trip through Postgres, not just the memory store.
//
// verifies SPEC §9.1
func TestARepeatSurvivesPostgres(t *testing.T) {
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
		t.Fatal(err)
	}
	const conv = "conv-oven-timer-repeat"
	if _, err := pool.Exec(ctx, `DELETE FROM journal_events WHERE conversation_id = $1`, conv); err != nil {
		t.Fatal(err)
	}

	store := journal.NewPgStore(pool)
	clk := &wallClock{}
	j := journal.New(store, clk, journal.Versions{Model: "qwen3:32b", Prompt: "sys@3", ToolSchema: "tools@7"})
	start := time.Unix(1_760_000_000, 0)
	for _, r := range []timed{
		{0, opened("kitchen", "alan")},
		{0.2, heard("set a timer for the oven", "alan")},
		{4.4, spoken("Sure, how long?")},
		{6.4, heard("set a timer for twelve minutes", "alan")},
	} {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		if _, err := j.Append(ctx, conv, r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	sigs, err := triage.Scan(ctx, store, conv)
	if err != nil {
		t.Fatal(err)
	}
	s := one(t, repeats(sigs))
	if s.Detail != "asked again 6.2 s after “set a timer for the oven” · no tool call on the first ask" {
		t.Errorf("detail = %q", s.Detail)
	}
}
