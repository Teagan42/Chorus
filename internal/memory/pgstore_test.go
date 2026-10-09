//go:build db

// Tagged db because `task test` is hermetic: no network, no docker, no
// Postgres. Run with `task test:db`.
package memory_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/memory"
)

// openPg skips without a database, unless the DSN was set explicitly, which
// is how CI cannot silently pass.
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
	if err := memory.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// verifies SPEC §5
func TestPgStoreConformance(t *testing.T) {
	pool := openPg(t)
	runStoreConformance(t, func(t *testing.T) memory.Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE memories, conversation_summaries`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return memory.NewPgStore(pool)
	})
}

// verifies SPEC §5
func TestMigrateIsIdempotent(t *testing.T) {
	pool := openPg(t)
	if err := memory.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// Past the Go check, the CHECK constraints are the last line of defence: a
// memory about nobody would be recalled to everybody who shares it.
//
// verifies SPEC §5
func TestPgStoreRefusesAMemoryAboutNobody(t *testing.T) {
	pool := openPg(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO memories VALUES ('m_00000000', '', 'Takes oat milk in coffee.', true, 'conv-raw', 'call_r1', now())`)
	if err == nil {
		t.Error("the database accepted a memory with no person")
	}
}

// A summary kept for nobody would be recalled to whoever has no id.
//
// verifies SPEC §5
func TestPgStoreRefusesASummaryForNobody(t *testing.T) {
	pool := openPg(t)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO conversation_summaries VALUES ('conv-raw', '', 'Somebody set a timer for the eggs.', now())`)
	if err == nil {
		t.Error("the database accepted a summary kept for nobody")
	}
}
