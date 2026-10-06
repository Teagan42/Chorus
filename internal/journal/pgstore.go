package journal

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

//go:embed migrations/*.sql
var migrations embed.FS

// DSNEnv names the environment variable holding the Postgres connection
// string. A password belongs there or in a gitignored .env, never in the repo.
const DSNEnv = "CHORUS_POSTGRES_DSN"

// DefaultDSN points at the docker-compose service. Local development only.
const DefaultDSN = "postgres://chorus:chorus@localhost:5432/chorus?sslmode=disable"

// DSN reads the configured connection string, falling back to the local
// docker-compose service.
func DSN() string {
	if dsn := os.Getenv(DSNEnv); dsn != "" {
		return dsn
	}
	return DefaultDSN
}

// Querier is the pgx surface PgStore needs, so a pool and a single connection
// are both usable.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgStore is the Postgres Store named by SPEC §8: JSONB payload, partitioned
// by conversation, sequence enforced by the database.
type PgStore struct {
	db Querier
}

// NewPgStore wraps an established connection or pool. Migrate must have run.
func NewPgStore(db Querier) *PgStore { return &PgStore{db: db} }

// Migrate applies every embedded migration not yet recorded, in name order.
// Deliberately not a framework: the ledger is one table and one query.
func Migrate(ctx context.Context, db Querier) error {
	const ledger = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text        PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`
	if _, err := db.Exec(ctx, ledger); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		err := db.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM schema_migrations WHERE version = $1)`, name,
		).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrations.ReadFile(path.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := db.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, name,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	return nil
}

// Insert succeeds only when seq is exactly one past the last. The WHERE
// rejects a gap; the primary key rejects a writer that raced past the WHERE.
const insertEvent = `
INSERT INTO journal_events (
	conversation_id, seq, kind, actor, wall_clock, speculative, audio_ref,
	model_version, prompt_version, tool_schema_version, fields)
SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
WHERE $2::bigint = (
	SELECT coalesce(max(seq), 0) + 1 FROM journal_events WHERE conversation_id = $1
)`

// Append enforces the monotonic, gapless sequence in the database, because two
// sessions writing one conversation cannot be ordered in Go.
func (p *PgStore) Append(ctx context.Context, e Event) error {
	if e.Seq > math.MaxInt64 {
		return fmt.Errorf("seq %d for %s: exceeds the bigint column", e.Seq, e.ConversationID)
	}
	fields, err := json.Marshal(nonNil(e.Fields))
	if err != nil {
		return fmt.Errorf("marshal fields for %s seq %d: %w", e.ConversationID, e.Seq, err)
	}

	tag, err := p.db.Exec(ctx, insertEvent,
		e.ConversationID, int64(e.Seq), string(e.Kind), string(e.Actor),
		// Explicit, though pgx also truncates: Postgres itself rounds, so the
		// resolution the log keeps must not depend on the driver.
		e.At.Truncate(StoredClockResolution), e.Speculative, e.AudioRef,
		e.Versions.Model, e.Versions.Prompt, e.Versions.ToolSchema, fields,
	)
	if err != nil {
		if seqTaken(err) {
			return p.seqError(ctx, e)
		}
		return fmt.Errorf("append %s seq %d: %w", e.ConversationID, e.Seq, err)
	}
	if tag.RowsAffected() == 0 {
		return p.seqError(ctx, e)
	}
	return nil
}

// seqError reports the sequence actually wanted, matching MemStore's message.
func (p *PgStore) seqError(ctx context.Context, e Event) error {
	last, err := p.LastSeq(ctx, e.ConversationID)
	if err != nil {
		return fmt.Errorf("seq %d for %s: rejected", e.Seq, e.ConversationID)
	}
	return fmt.Errorf("seq %d for %s: want %d", e.Seq, e.ConversationID, last+1)
}

// seqTaken reports a unique or check violation, both of which mean the
// sequence is unusable rather than the database being broken.
func seqTaken(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "23505" || pgErr.Code == "23514"
}

const selectEvents = `
SELECT seq, kind, actor, wall_clock, speculative, audio_ref,
       model_version, prompt_version, tool_schema_version, fields
FROM journal_events WHERE conversation_id = $1 ORDER BY seq`

// Events returns one conversation's log in sequence order. ORDER BY is
// load-bearing: heap order is not insertion order after any update or vacuum.
func (p *PgStore) Events(ctx context.Context, conversationID string) ([]Event, error) {
	rows, err := p.db.Query(ctx, selectEvents, conversationID)
	if err != nil {
		return nil, fmt.Errorf("events %s: %w", conversationID, err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var (
			e      = Event{ConversationID: conversationID}
			seq    int64
			at     time.Time
			fields []byte
		)
		if err := rows.Scan(
			&seq, &e.Kind, &e.Actor, &at, &e.Speculative, &e.AudioRef,
			&e.Versions.Model, &e.Versions.Prompt, &e.Versions.ToolSchema, &fields,
		); err != nil {
			return nil, fmt.Errorf("scan event %s: %w", conversationID, err)
		}
		if err := json.Unmarshal(fields, &e.Fields); err != nil {
			return nil, fmt.Errorf("unmarshal fields %s seq %d: %w", conversationID, seq, err)
		}
		e.Seq = uint64(seq)
		e.At = at.UTC()
		e.Fields = nonNil(e.Fields)
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("events %s: %w", conversationID, err)
	}
	return events, nil
}

// LastSeq reports 0 for an unknown conversation.
func (p *PgStore) LastSeq(ctx context.Context, conversationID string) (uint64, error) {
	var last int64
	err := p.db.QueryRow(ctx,
		`SELECT coalesce(max(seq), 0) FROM journal_events WHERE conversation_id = $1`,
		conversationID,
	).Scan(&last)
	if err != nil {
		return 0, fmt.Errorf("last seq %s: %w", conversationID, err)
	}
	return uint64(last), nil
}

// nonNil keeps an absent payload indistinguishable across backends; a nil map
// and an empty one must not reduce differently.
func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
