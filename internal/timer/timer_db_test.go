//go:build db

package timer_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/timer"
)

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
	// The house log alone: other packages' conversations are theirs.
	if _, err := pool.Exec(ctx, `DELETE FROM journal_events WHERE conversation_id = $1`, journal.HouseTimers); err != nil {
		t.Fatalf("clear the house log: %v", err)
	}
	return pool
}

// Alan sets the oven with the house log in Postgres, as chorusd keeps it,
// and the daemon restarts five minutes in. The new daemon reads the timer
// back from the database, and the kitchen hears it at 18:52 as set.
//
// verifies SPEC §8
func TestTheOvenTimerOutlivesTheDaemonInPostgres(t *testing.T) {
	h := newHouse("kitchen")
	h.store = journal.NewPgStore(openPg(t))
	first, stop := h.start(t)
	set, err := first.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 720, Label: "oven"})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(5 * time.Minute)
	stop()

	second, _ := h.start(t)
	if got := second.Running(); len(got) != 1 || got[0].ID != set.ID || !got[0].FiresAt.Equal(supper.Add(12*time.Minute)) {
		t.Fatalf("after the restart, running = %+v, want %s due at 18:52", got, set.ID)
	}
	await(t, "the oven to be armed again", func() bool { return h.clock.armed() > 0 })
	h.clock.advance(7 * time.Minute)

	if done := h.finished(t, 1); done.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v", done.Fields)
	}
	st, err := journal.Replay(context.Background(), h.store, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay the house log from Postgres: %v", err)
	}
	if len(st.Running()) != 0 || len(st.Timers) != 1 || st.Timers[0].Status != journal.TimerFinished {
		t.Errorf("replayed timers = %+v, want the oven, finished", st.Timers)
	}
}
