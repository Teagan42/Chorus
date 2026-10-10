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
		if _, err := pool.Exec(context.Background(), `TRUNCATE curation_decisions, curation_annotations, curation_promotions, curation_reruns`); err != nil {
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

// verifies SPEC §9.2
func TestPgStoreRefusesALabelOutsideTheVocabulary(t *testing.T) {
	pool := openPg(t)
	// Past the Go check, the CHECK constraint holds the vocabulary.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO curation_annotations
		VALUES ('conv-0853-kitchen', 2, ARRAY['rude'], '', now())`)
	if err == nil {
		t.Error("the database accepted label \"rude\"")
	}
}

// verifies SPEC §9.2
func TestPgStoreRefusesATakeThatDoesNothing(t *testing.T) {
	pool := openPg(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `TRUNCATE curation_promotions`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// Past the Go check, the CHECK constraint wants speech or a call.
	insert := `INSERT INTO curation_promotions
		VALUES ('conv-0930-office', 2, ' ', $1, 'qwen3-32b@1', 'sys@edited', 'tools@7', '', now())`
	if _, err := pool.Exec(ctx, insert, `[]`); err == nil {
		t.Error("the database accepted a take with no speech and no calls")
	}
	if _, err := pool.Exec(ctx, insert, `[{"tool":"ha_get_state","args":"{\"entity_id\":\"binary_sensor.garage_door_contact\"}"}]`); err != nil {
		t.Errorf("the database refused a take that only calls: %v", err)
	}
}

// verifies SPEC §9.2
func TestPgStoreRefusesAReRunThatRanNothing(t *testing.T) {
	pool := openPg(t)
	// Past the Go check, the CHECK constraint wants a turn that ran.
	_, err := pool.Exec(context.Background(), `
		INSERT INTO curation_reruns (conversation_id, model, prompt_version, tool_schema,
			system_prompt, tool_schema_json, takes_json, ran_at)
		VALUES ('conv-0853-kitchen', 'qwen3-32b@1', 'sys@edited', 'tools@7', '', '', '[]', now())`)
	if err == nil {
		t.Error("the database accepted a re-run of no turns")
	}
}
