package curation

import (
	"context"
	"embed"
	"fmt"
	"path"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/teaganglenn/chorus/internal/journal"
)

//go:embed migrations/*.sql
var migrations embed.FS

// PgStore keeps decisions in the journal's database, one row per pair, the
// last write winning by upsert.
type PgStore struct {
	db journal.Querier
}

// NewPgStore wraps an established connection or pool. Migrate must have run.
func NewPgStore(db journal.Querier) *PgStore { return &PgStore{db: db} }

// Migrate applies this package's migrations through the shared ledger, so
// one `schema_migrations` table records the whole database's history.
func Migrate(ctx context.Context, db journal.Querier) error {
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
		// Namespaced in the ledger: the journal's 0001 is not this 0001.
		version := "curation/" + name
		var applied bool
		err := db.QueryRow(ctx,
			`SELECT exists(SELECT 1 FROM schema_migrations WHERE version = $1)`, version,
		).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := migrations.ReadFile(path.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", version, err)
		}
		if _, err := db.Exec(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}
		if _, err := db.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}
	return nil
}

// Put stores or replaces the decision on its pair.
func (p *PgStore) Put(ctx context.Context, d Decision) error {
	if err := d.validate(); err != nil {
		return err
	}
	_, err := p.db.Exec(ctx, `
		INSERT INTO curation_decisions (
			pair_id, conversation_id, status, chosen, unfixed, reason, prev_status, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (pair_id) DO UPDATE SET
			conversation_id = excluded.conversation_id,
			status          = excluded.status,
			chosen          = excluded.chosen,
			unfixed         = excluded.unfixed,
			reason          = excluded.reason,
			prev_status     = excluded.prev_status,
			decided_at      = excluded.decided_at`,
		d.PairID, d.ConversationID, string(d.Status), d.Chosen, d.Unfixed, d.Reason, string(d.Prev), d.DecidedAt)
	if err != nil {
		return fmt.Errorf("put decision %s: %w", d.PairID, err)
	}
	return nil
}

// Delete returns the pair to unreviewed.
func (p *PgStore) Delete(ctx context.Context, pairID string) error {
	if _, err := p.db.Exec(ctx,
		`DELETE FROM curation_decisions WHERE pair_id = $1`, pairID); err != nil {
		return fmt.Errorf("delete decision %s: %w", pairID, err)
	}
	return nil
}

const selectDecision = `
	SELECT pair_id, conversation_id, status, chosen, unfixed, reason, prev_status, decided_at
	FROM curation_decisions`

func scanDecision(row pgx.CollectableRow) (Decision, error) {
	var d Decision
	var status, prev string
	err := row.Scan(&d.PairID, &d.ConversationID, &status, &d.Chosen, &d.Unfixed, &d.Reason, &prev, &d.DecidedAt)
	d.Status, d.Prev = Status(status), Status(prev)
	d.DecidedAt = d.DecidedAt.UTC()
	return d, err
}

// Get reports ok false for a pair nobody has reviewed.
func (p *PgStore) Get(ctx context.Context, pairID string) (Decision, bool, error) {
	rows, err := p.db.Query(ctx, selectDecision+` WHERE pair_id = $1`, pairID)
	if err != nil {
		return Decision{}, false, fmt.Errorf("get decision %s: %w", pairID, err)
	}
	ds, err := pgx.CollectRows(rows, scanDecision)
	if err != nil {
		return Decision{}, false, fmt.Errorf("get decision %s: %w", pairID, err)
	}
	if len(ds) == 0 {
		return Decision{}, false, nil
	}
	return ds[0], true, nil
}

// ForConversation returns the conversation's decisions keyed by pair id.
func (p *PgStore) ForConversation(ctx context.Context, conversationID string) (map[string]Decision, error) {
	rows, err := p.db.Query(ctx, selectDecision+` WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("decisions for %s: %w", conversationID, err)
	}
	ds, err := pgx.CollectRows(rows, scanDecision)
	if err != nil {
		return nil, fmt.Errorf("decisions for %s: %w", conversationID, err)
	}
	out := make(map[string]Decision, len(ds))
	for _, d := range ds {
		out[d.PairID] = d
	}
	return out, nil
}
