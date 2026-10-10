package curation

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/teagan42/chorus/internal/journal"
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
	return journal.ApplyMigrations(ctx, db, migrations, "curation/")
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

// PutAnnotation upserts the turn's annotation, or deletes an empty one.
func (p *PgStore) PutAnnotation(ctx context.Context, a Annotation) error {
	if err := a.validate(); err != nil {
		return err
	}
	if a.Empty() {
		if _, err := p.db.Exec(ctx,
			`DELETE FROM curation_annotations WHERE conversation_id = $1 AND turn_seq = $2`,
			a.ConversationID, int64(a.Seq)); err != nil {
			return fmt.Errorf("delete annotation %s/%d: %w", a.ConversationID, a.Seq, err)
		}
		return nil
	}
	labels := make([]string, len(a.Labels))
	for i, l := range a.Labels {
		labels[i] = string(l)
	}
	_, err := p.db.Exec(ctx, `
		INSERT INTO curation_annotations (conversation_id, turn_seq, labels, should_have, annotated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (conversation_id, turn_seq) DO UPDATE SET
			labels       = excluded.labels,
			should_have  = excluded.should_have,
			annotated_at = excluded.annotated_at`,
		a.ConversationID, int64(a.Seq), labels, a.ShouldHave, a.AnnotatedAt)
	if err != nil {
		return fmt.Errorf("put annotation %s/%d: %w", a.ConversationID, a.Seq, err)
	}
	return nil
}

// Annotations returns the conversation's annotations keyed by turn seq.
func (p *PgStore) Annotations(ctx context.Context, conversationID string) (map[uint64]Annotation, error) {
	rows, err := p.db.Query(ctx, `
		SELECT conversation_id, turn_seq, labels, should_have, annotated_at
		FROM curation_annotations WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("annotations for %s: %w", conversationID, err)
	}
	as, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Annotation, error) {
		var (
			a      Annotation
			seq    int64
			labels []string
		)
		err := row.Scan(&a.ConversationID, &seq, &labels, &a.ShouldHave, &a.AnnotatedAt)
		a.Seq, a.AnnotatedAt = uint64(seq), a.AnnotatedAt.UTC()
		for _, l := range labels {
			a.Labels = append(a.Labels, Label(l))
		}
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("annotations for %s: %w", conversationID, err)
	}
	out := make(map[uint64]Annotation, len(as))
	for _, a := range as {
		out[a.Seq] = a
	}
	return out, nil
}

// PutPromotion upserts the turn's promoted take.
func (p *PgStore) PutPromotion(ctx context.Context, pr Promotion) error {
	if err := pr.validate(); err != nil {
		return err
	}
	calls := pr.Calls
	if calls == nil {
		calls = []Call{}
	}
	callsJSON, err := json.Marshal(calls)
	if err != nil {
		return fmt.Errorf("put promotion %s/%d: %w", pr.ConversationID, pr.Seq, err)
	}
	_, err = p.db.Exec(ctx, `
		INSERT INTO curation_promotions (
			conversation_id, turn_seq, speech, calls_json, model, prompt_version, tool_schema,
			system_prompt, promoted_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (conversation_id, turn_seq) DO UPDATE SET
			speech         = excluded.speech,
			calls_json     = excluded.calls_json,
			model          = excluded.model,
			prompt_version = excluded.prompt_version,
			tool_schema    = excluded.tool_schema,
			system_prompt  = excluded.system_prompt,
			promoted_at    = excluded.promoted_at`,
		pr.ConversationID, int64(pr.Seq), pr.Speech, callsJSON,
		pr.Versions.Model, pr.Versions.Prompt, pr.Versions.ToolSchema, pr.SystemPrompt, pr.PromotedAt)
	if err != nil {
		return fmt.Errorf("put promotion %s/%d: %w", pr.ConversationID, pr.Seq, err)
	}
	return nil
}

// Promotions returns the conversation's promoted takes keyed by turn seq.
func (p *PgStore) Promotions(ctx context.Context, conversationID string) (map[uint64]Promotion, error) {
	rows, err := p.db.Query(ctx, `
		SELECT conversation_id, turn_seq, speech, calls_json, model, prompt_version, tool_schema,
			system_prompt, promoted_at
		FROM curation_promotions WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return nil, fmt.Errorf("promotions for %s: %w", conversationID, err)
	}
	ps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Promotion, error) {
		var (
			pr    Promotion
			seq   int64
			calls []byte
		)
		if err := row.Scan(&pr.ConversationID, &seq, &pr.Speech, &calls,
			&pr.Versions.Model, &pr.Versions.Prompt, &pr.Versions.ToolSchema,
			&pr.SystemPrompt, &pr.PromotedAt); err != nil {
			return pr, err
		}
		pr.Seq, pr.PromotedAt = uint64(seq), pr.PromotedAt.UTC()
		if err := json.Unmarshal(calls, &pr.Calls); err != nil {
			return pr, fmt.Errorf("calls_json: %w", err)
		}
		if len(pr.Calls) == 0 {
			pr.Calls = nil
		}
		return pr, nil
	})
	if err != nil {
		return nil, fmt.Errorf("promotions for %s: %w", conversationID, err)
	}
	out := make(map[uint64]Promotion, len(ps))
	for _, pr := range ps {
		out[pr.Seq] = pr
	}
	return out, nil
}
