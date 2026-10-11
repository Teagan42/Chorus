// Package erasure records what a reviewer deleted: what, when and how much,
// never what it held (ADR-0070). It sits beside the journal, as curation
// does, because the log a deletion removes cannot carry the record that it
// was removed, and a memory never was in the log.
package erasure

import (
	"context"
	"embed"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/teagan42/chorus/internal/journal"
)

// Kind is what sort of thing went.
type Kind string

const (
	// KindConversation is a conversation deleted whole: log, audio,
	// curation rows and summaries.
	KindConversation Kind = "conversation"
	// KindAudio is one event's clips, recorded as redacted in the log.
	KindAudio Kind = "audio"
	// KindMemory is a fact someone asked to be remembered.
	KindMemory Kind = "memory"
	// KindSummary is a conversation's summary kept for one person.
	KindSummary Kind = "summary"
)

// Record is one deletion. It names things by id and counts them; it never
// copies a transcript, a fact or a summary.
type Record struct {
	ID   uint64
	Kind Kind
	// ConversationID is the conversation deleted, the log the audio was
	// in, or the conversation a memory or summary came from.
	ConversationID string
	// Subject is what went within it: the clips' refs, or the memory's id.
	Subject string
	// Person is whose memory or summary it was.
	Person string
	// Counted is how much went with it, such as "5 events, 3 clips".
	Counted string
	At      time.Time
}

func (r Record) validate() error {
	switch r.Kind {
	case KindConversation, KindAudio, KindMemory, KindSummary:
	default:
		return fmt.Errorf("erasure: unknown kind %q", r.Kind)
	}
	switch {
	case r.ConversationID == "":
		return fmt.Errorf("erasure: %s with no conversation", r.Kind)
	case r.At.IsZero():
		return fmt.Errorf("erasure: %s of %s with no time", r.Kind, r.ConversationID)
	case (r.Kind == KindMemory || r.Kind == KindSummary) && r.Person == "":
		return fmt.Errorf("erasure: %s of %s with nobody it was about", r.Kind, r.ConversationID)
	}
	return nil
}

// Store keeps the records. Nothing deletes one: it is the trail.
type Store interface {
	// Add keeps a record and returns the id the store gave it.
	Add(ctx context.Context, r Record) (uint64, error)
	// List returns every record, newest first.
	List(ctx context.Context) ([]Record, error)
}

// MemStore is the in-memory Store used by tests and the demo.
type MemStore struct {
	mu      sync.Mutex
	records []Record
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore { return &MemStore{} }

// Add keeps r.
func (m *MemStore) Add(_ context.Context, r Record) (uint64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = uint64(len(m.records) + 1)
	r.At = r.At.Truncate(journal.StoredClockResolution).UTC()
	m.records = append(m.records, r)
	return r.ID, nil
}

// List returns every record, newest first.
func (m *MemStore) List(context.Context) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.records)
	slices.SortStableFunc(out, newestFirst)
	return out, nil
}

func newestFirst(a, b Record) int {
	if c := b.At.Compare(a.At); c != 0 {
		return c
	}
	switch {
	case a.ID > b.ID:
		return -1
	case a.ID < b.ID:
		return 1
	}
	return 0
}

//go:embed migrations/*.sql
var migrations embed.FS

// PgStore keeps records in the journal's database.
type PgStore struct {
	db journal.Querier
}

var (
	_ Store = (*MemStore)(nil)
	_ Store = (*PgStore)(nil)
)

// NewPgStore wraps an established connection or pool. Migrate must have run.
func NewPgStore(db journal.Querier) *PgStore { return &PgStore{db: db} }

// Migrate applies this package's migrations through the shared ledger.
func Migrate(ctx context.Context, db journal.Querier) error {
	return journal.ApplyMigrations(ctx, db, migrations, "erasure/")
}

// Add keeps r.
func (p *PgStore) Add(ctx context.Context, r Record) (uint64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	var id int64
	err := p.db.QueryRow(ctx, `
		INSERT INTO erasures (kind, conversation_id, subject, person, counted, erased_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id`,
		string(r.Kind), r.ConversationID, r.Subject, r.Person, r.Counted, r.At).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record the %s erased from %s: %w", r.Kind, r.ConversationID, err)
	}
	return uint64(id), nil
}

// List returns every record, newest first.
func (p *PgStore) List(ctx context.Context) ([]Record, error) {
	rows, err := p.db.Query(ctx, `
		SELECT id, kind, conversation_id, subject, person, counted, erased_at
		FROM erasures
		ORDER BY erased_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list erasures: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Record, error) {
		var (
			r    Record
			id   int64
			kind string
		)
		err := row.Scan(&id, &kind, &r.ConversationID, &r.Subject, &r.Person, &r.Counted, &r.At)
		r.ID, r.Kind, r.At = uint64(id), Kind(kind), r.At.UTC()
		return r, err
	})
	if err != nil {
		return nil, fmt.Errorf("list erasures: %w", err)
	}
	return out, nil
}
