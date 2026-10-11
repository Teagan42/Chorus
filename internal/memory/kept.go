package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// Kept is a Store as a reviewer reads it: everything kept, whoever it is
// about, shared or not, and a summary forgotten as a memory is (ADR-0070).
type Kept interface {
	// AllMemories returns every memory, by person, then newest first.
	AllMemories(ctx context.Context) ([]Memory, error)
	// AllSummaries returns every summary, by person, then newest first.
	AllSummaries(ctx context.Context) ([]Summary, error)
	// ForgetSummary deletes one conversation's summary for one person, and
	// reports whether one was kept.
	ForgetSummary(ctx context.Context, conversationID, person string) (bool, error)
	// ForgetSummaries deletes a conversation's summaries for everyone in it.
	ForgetSummaries(ctx context.Context, conversationID string) error
}

var (
	_ Kept = (*MemStore)(nil)
	_ Kept = (*PgStore)(nil)
)

// byPerson orders memories as a reviewer reads them.
func byPerson(a, b Memory) int {
	if c := cmp.Compare(a.Person, b.Person); c != 0 {
		return c
	}
	return newestFirst(a, b)
}

// summariesByPerson orders summaries as a reviewer reads them.
func summariesByPerson(a, b Summary) int {
	if c := cmp.Compare(a.Person, b.Person); c != 0 {
		return c
	}
	if c := b.At.Compare(a.At); c != 0 {
		return c
	}
	return cmp.Compare(a.ConversationID, b.ConversationID)
}

// AllMemories returns every memory.
func (s *MemStore) AllMemories(context.Context) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Memory, 0, len(s.byID))
	for _, m := range s.byID {
		out = append(out, m)
	}
	slices.SortFunc(out, byPerson)
	return out, nil
}

// AllSummaries returns every summary.
func (s *MemStore) AllSummaries(context.Context) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.summaries))
	for _, sum := range s.summaries {
		out = append(out, sum)
	}
	slices.SortFunc(out, summariesByPerson)
	return out, nil
}

// ForgetSummary deletes the person's summary of the conversation.
func (s *MemStore) ForgetSummary(_ context.Context, conversationID, person string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := summaryKey{conversationID, person}
	_, ok := s.summaries[k]
	delete(s.summaries, k)
	return ok, nil
}

// ForgetSummaries deletes the conversation's summaries.
func (s *MemStore) ForgetSummaries(_ context.Context, conversationID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.summaries {
		if k.conversation == conversationID {
			delete(s.summaries, k)
		}
	}
	return nil
}

// AllMemories returns every memory.
func (p *PgStore) AllMemories(ctx context.Context) ([]Memory, error) {
	rows, err := p.db.Query(ctx, `
		SELECT id, person, fact, shareable, conversation_id, call_id, remembered_at
		FROM memories
		ORDER BY person COLLATE "C", remembered_at DESC, id COLLATE "C"`)
	if err != nil {
		return nil, fmt.Errorf("read every memory: %w", err)
	}
	ms, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Memory, error) {
		var m Memory
		err := row.Scan(&m.ID, &m.Person, &m.Fact, &m.Shareable, &m.ConversationID, &m.CallID, &m.At)
		m.At = m.At.UTC()
		return m, err
	})
	if err != nil {
		return nil, fmt.Errorf("read every memory: %w", err)
	}
	return ms, nil
}

// AllSummaries returns every summary.
func (p *PgStore) AllSummaries(ctx context.Context) ([]Summary, error) {
	rows, err := p.db.Query(ctx, `
		SELECT conversation_id, person, summary, heard_at
		FROM conversation_summaries
		ORDER BY person COLLATE "C", heard_at DESC, conversation_id COLLATE "C"`)
	if err != nil {
		return nil, fmt.Errorf("read every summary: %w", err)
	}
	ss, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Summary, error) {
		var s Summary
		err := row.Scan(&s.ConversationID, &s.Person, &s.Text, &s.At)
		s.At = s.At.UTC()
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("read every summary: %w", err)
	}
	return ss, nil
}

// ForgetSummary deletes the person's summary of the conversation by key.
func (p *PgStore) ForgetSummary(ctx context.Context, conversationID, person string) (bool, error) {
	tag, err := p.db.Exec(ctx,
		`DELETE FROM conversation_summaries WHERE conversation_id = $1 AND person = $2`, conversationID, person)
	if err != nil {
		return false, fmt.Errorf("forget %s's summary of %s: %w", person, conversationID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// ForgetSummaries deletes the conversation's summaries, by the key's prefix.
func (p *PgStore) ForgetSummaries(ctx context.Context, conversationID string) error {
	if _, err := p.db.Exec(ctx,
		`DELETE FROM conversation_summaries WHERE conversation_id = $1`, conversationID); err != nil {
		return fmt.Errorf("forget the summaries of %s: %w", conversationID, err)
	}
	return nil
}
