//go:build db

// Tagged db because `task test` is hermetic. Run with `task test:db`.
package erasure_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/journal"
)

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
	if err := erasure.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

// verifies SPEC §8
func TestPgStoreConformance(t *testing.T) {
	pool := openPg(t)
	runStoreConformance(t, func(t *testing.T) erasure.Store {
		if _, err := pool.Exec(context.Background(), `TRUNCATE erasures`); err != nil {
			t.Fatalf("truncate: %v", err)
		}
		return erasure.NewPgStore(pool)
	})
}

// verifies SPEC §8
func TestMigrateIsIdempotent(t *testing.T) {
	pool := openPg(t)
	if err := erasure.Migrate(context.Background(), pool); err != nil {
		t.Errorf("second migrate: %v", err)
	}
}

// The table refuses a kind the Go side never writes, so a record cannot
// say something went that the review UI has no words for.
//
// verifies SPEC §8
func TestPgStoreRefusesAnUnknownKind(t *testing.T) {
	pool := openPg(t)
	_, err := pool.Exec(context.Background(),
		`INSERT INTO erasures (kind, conversation_id, subject, person, counted, erased_at) VALUES ('photo', 'conv-x', '', '', '', now())`)
	if err == nil {
		t.Error("the table took a kind nothing writes")
	}
}
