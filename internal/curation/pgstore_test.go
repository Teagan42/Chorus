//go:build db

// Tagged db because `task test` is hermetic: no network, no docker, no
// Postgres. Run with `task test:db`.
package curation_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
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
	if err := curation.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// verifies SPEC §9.2
func TestPgStoreConformance(t *testing.T) {
	pool := openPg(t)

	// The suite's cases share pair ids, so each gets an empty table rather
	// than a namespaced store - the store under test must be the real one.
	runStoreConformance(t, func(t *testing.T) curation.Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE curation_decisions`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return curation.NewPgStore(pool)
	})
}

// verifies SPEC §9.2
func TestMigrateIsIdempotent(t *testing.T) {
	pool := openPg(t)
	if err := curation.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// verifies SPEC §9.2
func TestPgStoreRefusesAStatusTheFlowNeverProduces(t *testing.T) {
	pool := openPg(t)
	// Past the Go check, the CHECK constraint is the last line of defence.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO curation_decisions
		VALUES ('conv-raw/1', 'conv-raw', 'unreviewed', '', false, '', now())`)
	if err == nil {
		t.Error("the database accepted status \"unreviewed\"")
	}
}
