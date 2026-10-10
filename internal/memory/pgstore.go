package memory

import (
	"context"
	"embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/chorus/internal/journal"
)

//go:embed migrations/*.sql
var migrations embed.FS

// PgStore keeps memories in the journal's database, one row each.
type PgStore struct {
	db journal.Querier
}

var _ Store = (*PgStore)(nil)

// NewPgStore wraps an established connection or pool. Migrate must have run.
func NewPgStore(db journal.Querier) *PgStore { return &PgStore{db: db} }

// Migrate applies this package's migrations through the shared ledger.
func Migrate(ctx context.Context, db journal.Querier) error {
	return journal.ApplyMigrations(ctx, db, migrations, "memory/")
}

// Remember stores m. A taken id is the primary key's to refuse.
func (p *PgStore) Remember(ctx context.Context, m Memory) error {
	if err := m.validate(); err != nil {
		return err
	}
	_, err := p.db.Exec(ctx, `
		INSERT INTO memories (id, person, fact, shareable, conversation_id, call_id, remembered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		m.ID, m.Person, m.Fact, m.Shareable, m.ConversationID, m.CallID, m.At)
	if err != nil {
		return fmt.Errorf("remember %s: %w", m.ID, err)
	}
	return nil
}

// Forget deletes the person's memory by id; another person's row is left alone.
func (p *PgStore) Forget(ctx context.Context, person, id string) (bool, error) {
	tag, err := p.db.Exec(ctx, `DELETE FROM memories WHERE id = $1 AND person = $2`, id, person)
	if err != nil {
		return false, fmt.Errorf("forget %s: %w", id, err)
	}
	return tag.RowsAffected() == 1, nil
}

// Recall returns what person may be told, newest first.
func (p *PgStore) Recall(ctx context.Context, person string, limit int) ([]Memory, error) {
	rows, err := p.db.Query(ctx, `
		SELECT id, person, fact, shareable, conversation_id, call_id, remembered_at
		FROM memories
		WHERE person = $1 OR shareable
		ORDER BY remembered_at DESC, id COLLATE "C"
		LIMIT $2`, person, limit)
	if err != nil {
		return nil, fmt.Errorf("recall %s: %w", person, err)
	}
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Memory, error) {
		var m Memory
		err := row.Scan(&m.ID, &m.Person, &m.Fact, &m.Shareable, &m.ConversationID, &m.CallID, &m.At)
		m.At = m.At.UTC()
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("recall %s: %w", person, err)
	}
	return ms, nil
}

// Summarized keeps s, unless a newer summary of the conversation is kept, and
// prunes the person's summaries older than SummaryKeep before it.
func (p *PgStore) Summarized(ctx context.Context, s Summary) error {
	if err := s.validate(); err != nil {
		return err
	}
	_, err := p.db.Exec(ctx, `
		WITH pruned AS (
			DELETE FROM conversation_summaries
			WHERE person = $2 AND heard_at < $5 AND conversation_id <> $1
		)
		INSERT INTO conversation_summaries (conversation_id, person, summary, heard_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (conversation_id, person) DO UPDATE
		SET summary = EXCLUDED.summary, heard_at = EXCLUDED.heard_at
		WHERE conversation_summaries.heard_at <= EXCLUDED.heard_at`,
		s.ConversationID, s.Person, s.Text, s.At, s.At.Add(-SummaryKeep))
	if err != nil {
		return fmt.Errorf("keep %s's summary of %s: %w", s.Person, s.ConversationID, err)
	}
	return nil
}

// Summaries returns the person's recent summaries, newest first.
func (p *PgStore) Summaries(ctx context.Context, person, except string, since time.Time, limit int) ([]Summary, error) {
	rows, err := p.db.Query(ctx, `
		SELECT conversation_id, person, summary, heard_at
		FROM conversation_summaries
		WHERE person = $1 AND conversation_id <> $2 AND heard_at >= $3
		ORDER BY heard_at DESC, conversation_id COLLATE "C"
		LIMIT $4`, person, except, since, limit)
	if err != nil {
		return nil, fmt.Errorf("recall %s's conversations: %w", person, err)
	}
	ss, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		err := row.Scan(&s.ConversationID, &s.Person, &s.Text, &s.At)
		s.At = s.At.UTC()
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("recall %s's conversations: %w", person, err)
	}
	return ss, nil
}
