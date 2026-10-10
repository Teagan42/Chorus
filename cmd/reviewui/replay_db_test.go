//go:build db

// Tagged db because `task test` is hermetic. Run with `task test:db`.
package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// openPg connects and migrates both schemas reviewui serves from. An
// unreachable database skips unless the DSN was set explicitly, so CI cannot
// silently pass.
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

// The Zeppelin barge-in, written to Postgres the way chorusd writes it and
// replayed through the page the way a reviewer does.
//
// verifies SPEC §9.2
func TestReplayOverPostgresRerunsTheRecordedTurns(t *testing.T) {
	pool := openPg(t)
	// A fixed id, cleared first: the suite shares one database, and a test
	// that reads the wall clock for a fresh id is the thing CONTRIBUTING bans.
	const conv = "conv-zeppelin-replay"
	if _, err := pool.Exec(context.Background(), `DELETE FROM journal_events WHERE conversation_id = $1`, conv); err != nil {
		t.Fatalf("clear %s: %v", conv, err)
	}
	j := journal.New(journal.NewPgStore(pool), journal.FixedClock(time.Unix(1_760_000_000, 0)),
		journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	for _, r := range zeppelinRecords() {
		if _, err := j.Append(context.Background(), conv, r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}

	s := newServer(pgJournal{journal.NewPgStore(pool)}, curation.NewPgStore(pool), fixtureBlobs(t), func() time.Time { return time.Unix(1_760_010_000, 0).UTC() })
	s.engineFor = leadsWithTheCount().engineFor

	missing(t, "Replay index", get(t, s, "/replays"), `href="/replays/`+conv+`"`)
	missing(t, "Replay page", get(t, s, replayHref(conv)),
		"play something by zeppelin", " albums by that artist", "replay() reads back all 13 events")
	if _, err := pool.Exec(context.Background(), `DELETE FROM curation_reruns WHERE conversation_id = $1`, conv); err != nil {
		t.Fatalf("clear %s's re-runs: %v", conv, err)
	}
	missing(t, "Replay result", runReplay(t, s, conv, url.Values{"model": {"qwen3:32b"}, "prompt": {cutFirstPrompt}, "tools": {searchBack()}}),
		"I found three albums. Want Led Zeppelin one?", "speech and calls changed", ">same</span>")

	// The run is kept in Postgres, and its take promoted from there.
	runs, err := s.decisions.Reruns(context.Background(), conv)
	if err != nil || len(runs) != 1 {
		t.Fatalf("kept re-runs = %v, %v; want the one", runs, err)
	}
	promote := fmt.Sprintf("%s/runs/%d/turns/2/promote", replayHref(conv), runs[0].ID)
	missing(t, "kept re-run", get(t, s, fmt.Sprintf("%s/runs/%d", replayHref(conv), runs[0].ID)), `hx-post="`+promote+`"`)
	mustPost(t, s, promote, nil)
	promos, err := s.decisions.Promotions(context.Background(), conv)
	if p := promos[2]; err != nil || p.Speech != "I found three albums. Want Led Zeppelin one?" || p.Versions.Prompt != "sys@edited" {
		t.Errorf("promotion = %+v, %v", p, err)
	}
}
