//go:build db

// Tagged db because `task test` is hermetic: no network, no docker, no
// Postgres. Run with `task test:db`.
package contexts

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

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
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pool
}

func emptied(t *testing.T, pool *pgxpool.Pool) *PgStore {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `TRUNCATE context_versions`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return NewPgStore(pool)
}

// verifies SPEC §8, §13
func TestPgStoreConformance(t *testing.T) {
	pool := openPg(t)
	runStoreConformance(t, func(t *testing.T) Store { return emptied(t, pool) })
}

// verifies SPEC §13
func TestMigrateIsIdempotent(t *testing.T) {
	pool := openPg(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// Someone edits the weather server's description in the database by hand.
// The row no longer hashes to its label, so it reads as never kept rather
// than as a context the model was not sent; the prompt beside it still
// resolves.
//
// verifies SPEC §13
func TestAHandEditedVersionReadsAsNeverKept(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	s := emptied(t, pool)
	o := houseOffer(t, "weather")
	prompt, tools, err := o.Versions()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Record(ctx, o); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE context_versions SET body = replace(body, 'Get the weather forecast', 'Get tomorrow''s forecast') WHERE kind = 'tool_schema'`,
	); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recorded(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.ToolSchema(tools); ok {
		t.Error("an edited schema still resolves its label")
	}
	if _, ok := got.Prompt(prompt); !ok {
		t.Error("the untouched prompt no longer resolves")
	}
}
