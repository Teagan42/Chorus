package contexts

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
)

// Offer is what a running chorusd offers its model: the persona, the
// summary prompt its label hashes beside it, and the tool declarations as
// ToolSchema writes them, the house's MCP tools among them. A build names
// only its own registry's tools, so an offer with a server's tools in it is
// a version only the process that offered it can record (ADR-0101).
type Offer struct {
	Prompt     string
	Summary    string
	ToolSchema string
}

// Versions are the labels an engine offering o records on every event
// (SPEC §8): the rule TestEveryArchivedPromptHashesToItsLabel and
// TestEveryArchivedToolSchemaHashesToItsLabel hold the archive to.
func (o Offer) Versions() (prompt, toolSchema string, err error) {
	if strings.TrimSpace(o.Prompt) == "" {
		return "", "", errors.New("contexts: an offer needs a prompt")
	}
	if strings.TrimSpace(o.ToolSchema) == "" {
		return "", "", errors.New("contexts: an offer needs its tool declarations")
	}
	toolSchema, err = toolSchemaVersion(o.ToolSchema)
	if err != nil {
		return "", "", err
	}
	return promptVersion(o.Prompt, o.Summary), toolSchema, nil
}

// promptVersion is the persona's label: alone, or with the summary prompt
// for a build that summarized, as chat.PromptVersion hashes the two.
func promptVersion(system, summary string) string {
	if summary == "" {
		return chat.Fingerprint([]byte(system))
	}
	return chat.Fingerprint([]byte(system + "\x00" + summary))
}

// toolSchemaVersion is the declarations' label: hashed as the wire carried
// them, compact. Marshalling a RawMessage compacts with the wire's escapes,
// so an indented schema hashes to what was sent.
func toolSchemaVersion(schema string) (string, error) {
	wire, err := json.Marshal(json.RawMessage(schema))
	if err != nil {
		return "", fmt.Errorf("contexts: the tool schema is not JSON: %w", err)
	}
	return chat.Fingerprint(wire), nil
}

// Store keeps the offers a running chorusd made, beside the journal.
type Store interface {
	// Record keeps o's text under its labels. A label already kept is left
	// as it is: its text is what the label hashed.
	Record(ctx context.Context, o Offer) error

	// Recorded is every version kept, as a Source. A row whose text does
	// not hash to its label is left out, so it reads as never kept: a label
	// nothing holds is never guessed at (ADR-0071).
	Recorded(ctx context.Context) (Table, error)
}

// MemStore is a Store in memory, for tests and the demo.
type MemStore struct {
	mu sync.Mutex
	t  Table
}

// NewMemStore keeps nothing yet.
func NewMemStore() *MemStore {
	return &MemStore{t: Table{Prompts: map[string]string{}, ToolSchemas: map[string]string{}}}
}

// Record keeps o's text under its labels.
func (m *MemStore) Record(_ context.Context, o Offer) error {
	prompt, tools, err := o.Versions()
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.t.Prompts[prompt]; !ok {
		m.t.Prompts[prompt] = o.Prompt
	}
	if _, ok := m.t.ToolSchemas[tools]; !ok {
		m.t.ToolSchemas[tools] = o.ToolSchema
	}
	return nil
}

// Recorded is a copy of what is kept.
func (m *MemStore) Recorded(context.Context) (Table, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := Table{Prompts: map[string]string{}, ToolSchemas: map[string]string{}}
	for k, v := range m.t.Prompts {
		out.Prompts[k] = v
	}
	for k, v := range m.t.ToolSchemas {
		out.ToolSchemas[k] = v
	}
	return out, nil
}

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies this package's migrations through the journal's shared
// ledger.
func Migrate(ctx context.Context, db journal.Querier) error {
	return journal.ApplyMigrations(ctx, db, migrations, "contexts/")
}

// PgStore keeps offers in the journal's database, one row per label.
type PgStore struct {
	db journal.Querier
}

// NewPgStore wraps an established connection or pool. Migrate must have run.
func NewPgStore(db journal.Querier) *PgStore { return &PgStore{db: db} }

// Record keeps o's text under its labels; a label already kept is not
// rewritten.
func (p *PgStore) Record(ctx context.Context, o Offer) error {
	prompt, tools, err := o.Versions()
	if err != nil {
		return err
	}
	const insert = `
		INSERT INTO context_versions (kind, version, body, summary)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (kind, version) DO NOTHING`
	if _, err := p.db.Exec(ctx, insert, "prompt", prompt, o.Prompt, o.Summary); err != nil {
		return fmt.Errorf("record prompt %s: %w", prompt, err)
	}
	if _, err := p.db.Exec(ctx, insert, "tool_schema", tools, o.ToolSchema, ""); err != nil {
		return fmt.Errorf("record tool schema %s: %w", tools, err)
	}
	return nil
}

// Recorded reads every kept version, each checked against its label.
func (p *PgStore) Recorded(ctx context.Context) (Table, error) {
	rows, err := p.db.Query(ctx, `SELECT kind, version, body, summary FROM context_versions`)
	if err != nil {
		return Table{}, fmt.Errorf("read context versions: %w", err)
	}
	defer rows.Close()
	t := Table{Prompts: map[string]string{}, ToolSchemas: map[string]string{}}
	for rows.Next() {
		var kind, version, body, summary string
		if err := rows.Scan(&kind, &version, &body, &summary); err != nil {
			return Table{}, fmt.Errorf("read context versions: %w", err)
		}
		switch kind {
		case "prompt":
			if promptVersion(body, summary) == version {
				t.Prompts[version] = body
			}
		case "tool_schema":
			if got, err := toolSchemaVersion(body); err == nil && got == version {
				t.ToolSchemas[version] = body
			}
		}
	}
	if err := rows.Err(); err != nil {
		return Table{}, fmt.Errorf("read context versions: %w", err)
	}
	return t, nil
}
