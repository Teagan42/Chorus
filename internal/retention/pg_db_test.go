//go:build db

// Tagged db because `task test` is hermetic. Run with `task test:db`.
package retention_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/retention"
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
	if err := curation.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate curation: %v", err)
	}
	return pool
}

// The Postgres delete path end to end, on a satellite no other package's
// fixtures name: a conversation past the year is deleted with its verdicts,
// a curated one is kept, and the satellite's own log is trimmed by primary
// key with its last event kept.
//
// verifies SPEC §8, §9.2, §9.3
func TestThePrunerDeletesFromPostgres(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	const sat = "db-retention-scullery"
	gone, kept, dev := "conv-"+sat+"-gone", "conv-"+sat+"-kept", "device:"+sat
	for _, q := range []string{
		`DELETE FROM journal_events WHERE conversation_id = ANY($1)`,
		`DELETE FROM curation_decisions WHERE conversation_id = ANY($1)`,
		`DELETE FROM curation_wake_verdicts WHERE conversation_id = ANY($1)`,
	} {
		if _, err := pool.Exec(ctx, q, []string{gone, kept, dev}); err != nil {
			t.Fatal(err)
		}
	}
	store, verdicts, blobs := journal.NewPgStore(pool), curation.NewPgStore(pool), blob.NewMemory()
	old := today.Add(-400 * day)
	write := func(at time.Time, conv string, records ...journal.Record) {
		t.Helper()
		j := journal.New(store, journal.FixedClock(at), versions)
		for _, r := range records {
			if _, err := j.Append(ctx, conv, r); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(old, gone, weather(sat, "pg-gone")...)
	write(old, kept, weather(sat, "pg-kept")...)
	write(old, dev,
		rec(journal.KindWakeRejected, "blob://wake/pg-cough", "reason", "no_speech"),
		rec(journal.KindWakeRejected, "blob://wake/pg-guest", "reason", "unknown_speaker"),
	)
	write(today.Add(-day), dev, rec(journal.KindPresenceChanged, "", "state", "present"))
	for _, err := range []error{
		verdicts.Put(ctx, curation.Decision{PairID: gone + "/3", ConversationID: gone, Status: curation.StatusDiscarded, Reason: "noise", DecidedAt: old}),
		verdicts.Put(ctx, curation.Decision{PairID: kept + "/3", ConversationID: kept, Status: curation.StatusAccepted, Chosen: "Rain from three.", DecidedAt: old}),
		verdicts.PutWakeVerdict(ctx, curation.WakeVerdict{ConversationID: dev, Seq: 1, Status: curation.WakeDiscarded, JudgedAt: old}),
		verdicts.PutWakeVerdict(ctx, curation.WakeVerdict{ConversationID: dev, Seq: 2, Status: curation.WakeConfirmed, JudgedAt: old}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	clk := &clock{now: today, asked: make(chan struct{}, 8)}
	p, err := retention.New(retention.Config{
		Journal: journal.New(store, clk, versions), Store: store, Blobs: blobs, Curation: verdicts,
		Clock: clk, Timers: clk,
		Policy: retention.Policy{Satellites: map[string]retention.Horizons{sat: {Journal: 365 * day}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Pass(ctx); err != nil {
		t.Fatalf("pass: %v", err)
	}

	if events, _ := store.Events(ctx, gone); len(events) != 0 {
		t.Errorf("the gone conversation keeps %d events", len(events))
	}
	if ds, _ := verdicts.ForConversation(ctx, gone); len(ds) != 0 {
		t.Errorf("the gone conversation keeps verdicts %v", ds)
	}
	if events, _ := store.Events(ctx, kept); len(events) == 0 {
		t.Error("the curated conversation was deleted")
	}
	events, err := store.Events(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	var seqs []uint64
	for _, e := range events {
		seqs = append(seqs, e.Seq)
	}
	if !slices.Equal(seqs, []uint64{2, 3}) {
		t.Errorf("device log seqs = %v, want the confirmed guest and the last presence", seqs)
	}
	if last, _ := store.LastSeq(ctx, dev); last != 3 {
		t.Errorf("last seq = %d, want 3", last)
	}
	vs, _ := verdicts.WakeVerdicts(ctx, dev)
	if _, ok := vs[1]; ok || len(vs) != 1 {
		t.Errorf("wake verdicts = %v, want only the confirmed one", vs)
	}
}
