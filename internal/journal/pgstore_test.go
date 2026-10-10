//go:build db

// Tagged db because `task test` is hermetic: no network, no docker, no
// Postgres. Run with `task test:db`.
package journal_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teagan42/chorus/internal/journal"
)

// convSeq keeps each subtest on conversation ids nothing else wrote, so one
// database serves the whole suite without a truncate between cases.
var convSeq atomic.Uint64

// openPg connects once per process and migrates. An unreachable database skips
// unless the DSN was set explicitly, which is how CI cannot silently pass.
func openPg(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), journal.DSN())
	if err == nil {
		err = pool.Ping(context.Background())
	}
	if err != nil {
		if os.Getenv(journal.DSNEnv) != "" {
			t.Fatalf("%s is set but unreachable: %v", journal.DSNEnv, err)
		}
		t.Skipf("no Postgres at the default DSN (%v); run `docker compose up -d postgres`", err)
	}
	t.Cleanup(pool.Close)

	if err := journal.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// verifies SPEC §8
func TestPgStoreConformance(t *testing.T) {
	pool := openPg(t)

	// The suite's cases share conversation ids, so each gets an empty table
	// rather than a namespaced store - the store under test must be the real
	// one, with the real ids.
	runStoreConformance(t, func(t *testing.T) journal.Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE journal_events`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return journal.NewPgStore(pool)
	})
}

// verifies SPEC §8
func TestPgStoreKeepsEachConversationInOnePartition(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	s := journal.NewPgStore(pool)
	conv := fmt.Sprintf("conv-partition-%d", convSeq.Add(1))

	for seq := uint64(1); seq <= 3; seq++ {
		if err := s.Append(ctx, journal.Event{
			Seq: seq, ConversationID: conv, Kind: journal.KindSessionClosed,
			Actor:  journal.Meta[journal.KindSessionClosed].Actor,
			Fields: map[string]string{"reason": "model_ended"},
		}); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}

	// One conversation whole in one partition is what makes HASH the right
	// choice; a read that touched every partition would not prune.
	var partitions int
	err := pool.QueryRow(ctx, `
		SELECT count(DISTINCT tableoid) FROM journal_events WHERE conversation_id = $1
	`, conv).Scan(&partitions)
	if err != nil {
		t.Fatalf("count partitions: %v", err)
	}
	if partitions != 1 {
		t.Errorf("conversation spans %d partitions, want 1", partitions)
	}
}

// verifies SPEC §8
func TestPgStoreRejectsADuplicateSequenceInTheDatabase(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	conv := fmt.Sprintf("conv-dup-%d", convSeq.Add(1))

	if err := journal.NewPgStore(pool).Append(ctx, journal.Event{
		Seq: 1, ConversationID: conv, Kind: journal.KindSessionClosed,
		Fields: map[string]string{"reason": "model_ended"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// Bypasses Go entirely. The gapless sequence is a database invariant, so a
	// writer that is not this package still cannot corrupt the log.
	_, err := pool.Exec(ctx, `
		INSERT INTO journal_events (conversation_id, seq, kind, actor, wall_clock,
			speculative, audio_ref, model_version, prompt_version,
			tool_schema_version, fields)
		VALUES ($1, 1, 'session_closed', 'session', now(), false, '', '', '', '', '{}')
	`, conv)
	if err == nil {
		t.Fatal("a raw INSERT rewrote seq 1; the constraint is only in Go")
	}
}

// verifies SPEC §8
func TestPgStoreRejectsANonPositiveSequence(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	conv := fmt.Sprintf("conv-zero-%d", convSeq.Add(1))

	_, err := pool.Exec(ctx, `
		INSERT INTO journal_events (conversation_id, seq, kind, actor, wall_clock,
			speculative, audio_ref, model_version, prompt_version,
			tool_schema_version, fields)
		VALUES ($1, 0, 'session_closed', 'session', now(), false, '', '', '', '', '{}')
	`, conv)
	if err == nil {
		t.Fatal("seq 0 was accepted; sequence numbers start at 1")
	}
}

// verifies SPEC §8
func TestPgStoreStoresAnAbsentPayloadAsAnEmptyObject(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	conv := fmt.Sprintf("conv-empty-%d", convSeq.Add(1))

	if err := journal.NewPgStore(pool).Append(ctx, journal.Event{
		Seq: 1, ConversationID: conv, Kind: journal.KindSessionClosed,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	// 'null'::jsonb satisfies NOT NULL but makes fields->>'x' return NULL for
	// every key, which the SQL readers of §8 cannot distinguish from a value.
	var typ string
	if err := pool.QueryRow(ctx,
		`SELECT jsonb_typeof(fields) FROM journal_events WHERE conversation_id = $1`, conv,
	).Scan(&typ); err != nil {
		t.Fatalf("jsonb_typeof: %v", err)
	}
	if typ != "object" {
		t.Errorf("fields is jsonb %s, want object", typ)
	}
}

// verifies SPEC §8
func TestPgStoreReadsALegacyNullPayloadAsAnEmptyMap(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	conv := fmt.Sprintf("conv-null-%d", convSeq.Add(1))

	// A backfill or another SQL writer can leave 'null'::jsonb. The reducer
	// must still see a usable payload rather than a nil map from the backend.
	if _, err := pool.Exec(ctx, `
		INSERT INTO journal_events (conversation_id, seq, kind, actor, wall_clock,
			speculative, audio_ref, model_version, prompt_version,
			tool_schema_version, fields)
		VALUES ($1, 1, 'session_closed', 'session', now(), false, '', '', '', '', 'null')
	`, conv); err != nil {
		t.Fatalf("seed null payload: %v", err)
	}

	events, err := journal.NewPgStore(pool).Events(ctx, conv)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if events[0].Fields == nil {
		t.Error("fields came back nil, want an empty map")
	}
}

// verifies SPEC §8
func TestPgStoreOrdersBySequenceNotHeapPosition(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	s := journal.NewPgStore(pool)
	conv := fmt.Sprintf("conv-order-%d", convSeq.Add(1))

	const n = 6
	for seq := uint64(1); seq <= n; seq++ {
		if err := s.Append(ctx, journal.Event{
			Seq: seq, ConversationID: conv, Kind: journal.KindSessionClosed,
			Fields: map[string]string{"reason": "model_ended"},
		}); err != nil {
			t.Fatalf("append %d: %v", seq, err)
		}
	}

	// Rewrites seq 1 to the end of the heap. Insertion order is not storage
	// order after any row rewrite, so a query without ORDER BY reads garbage.
	if _, err := pool.Exec(ctx,
		`UPDATE journal_events SET audio_ref = 'rewritten' WHERE conversation_id = $1 AND seq = 1`,
		conv,
	); err != nil {
		t.Fatalf("rewrite row: %v", err)
	}

	events, err := s.Events(ctx, conv)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != n {
		t.Fatalf("got %d events, want %d", len(events), n)
	}
	for i, e := range events {
		if want := uint64(i + 1); e.Seq != want {
			t.Fatalf("position %d holds seq %d, want %d", i, e.Seq, want)
		}
	}
}

// legacySchema hands back a connection whose search_path is a fresh schema at
// migration 0001 alone, as a household's database stood before 0002, so later
// migrations run over real rows.
func legacySchema(t *testing.T, pool *pgxpool.Pool) *pgxpool.Conn {
	t.Helper()
	ctx := context.Background()

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	t.Cleanup(conn.Release)

	name := fmt.Sprintf("legacy_%d", convSeq.Add(1))
	if _, err := conn.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	// Reset before release, or the next test to draw this connection migrates
	// into this schema.
	t.Cleanup(func() {
		if _, err := conn.Exec(ctx, "RESET search_path"); err != nil {
			t.Errorf("reset search_path: %v", err)
		}
		if _, err := conn.Exec(ctx, "DROP SCHEMA "+name+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
	})
	if _, err := conn.Exec(ctx, "SET search_path TO "+name); err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	first, err := os.ReadFile("migrations/0001_journal_events.sql")
	if err != nil {
		t.Fatalf("read migration 0001: %v", err)
	}
	if _, err := conn.Exec(ctx, string(first)); err != nil {
		t.Fatalf("apply migration 0001: %v", err)
	}
	// The ledger as the previous build left it: Migrate skips 0001, applies the rest.
	if _, err := conn.Exec(ctx, `
		CREATE TABLE schema_migrations (
			version    text        PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now());
		INSERT INTO schema_migrations (version) VALUES ('0001_journal_events.sql')
	`); err != nil {
		t.Fatalf("seed ledger: %v", err)
	}
	return conn
}

// verifies SPEC §8
func TestPgStoreReadsARowFromBeforeTheSTTAndTTSColumnsAsEmpty(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	conn := legacySchema(t, pool)
	const conv = "conv-legacy"

	// No STT or TTS column exists yet; 0002 must add them around rows on disk.
	for seq := 1; seq <= 2; seq++ {
		if _, err := conn.Exec(ctx, `
			INSERT INTO journal_events (conversation_id, seq, kind, actor, wall_clock,
				speculative, audio_ref, model_version, prompt_version,
				tool_schema_version, fields)
			VALUES ($1, $2, 'session_closed', 'session', now(), false, '',
				'qwen3-32b@1', 'sys@3', 'tools@7', '{"reason":"model_ended"}')
		`, conv, seq); err != nil {
			t.Fatalf("seed legacy row %d: %v", seq, err)
		}
	}

	if err := journal.Migrate(ctx, conn); err != nil {
		t.Fatalf("migrate over legacy rows: %v", err)
	}
	s := journal.NewPgStore(conn)

	events, err := s.Events(ctx, conv)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want the 2 legacy rows", len(events))
	}
	for _, e := range events {
		want := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
		if e.Versions != want {
			t.Errorf("seq %d versions = %+v, want %+v with empty STT and TTS", e.Seq, e.Versions, want)
		}
	}

	// Go reads NULL as ""; SQL still needs the NULL to tell unrecorded from unconfigured.
	var nulls int
	if err := conn.QueryRow(ctx, `
		SELECT count(*) FROM journal_events
		WHERE conversation_id = $1 AND stt_version IS NULL AND tts_version IS NULL
	`, conv).Scan(&nulls); err != nil {
		t.Fatalf("count nulls: %v", err)
	}
	if nulls != 2 {
		t.Errorf("%d legacy rows hold NULL, want 2; the migration backfilled", nulls)
	}

	// A conversation that spans the upgrade mixes NULL rows with stamped ones.
	stamped := event(conv, 3)
	stamped.Versions = versions()
	if err := s.Append(ctx, stamped); err != nil {
		t.Fatalf("append after migration: %v", err)
	}
	events, err = s.Events(ctx, conv)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	if got := events[2].Versions; got != versions() {
		t.Errorf("versions = %+v, want %+v stamped after the migration", got, versions())
	}
}

// verifies SPEC §8
func TestMigrateIsIdempotent(t *testing.T) {
	pool := openPg(t)

	// openPg already migrated. A second run must be a no-op, or a restart of
	// the orchestrator fails on its own schema.
	if err := journal.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}
