package curation

import (
	"context"
	"fmt"
	"slices"
)

// Forgetter is a Store retention may delete from, once the log a row
// names is gone (ADR-0065). Nothing else forgets: a row is a reviewer's.
type Forgetter interface {
	// Forget removes every row of one conversation: verdicts, labels,
	// promotions and re-runs. No row depends on another, so no order.
	Forget(ctx context.Context, conversationID string) error
	// ForgetWakes removes the verdicts on the named events of a device log.
	ForgetWakes(ctx context.Context, conversationID string, seqs []uint64) error
}

var (
	_ Forgetter = (*MemStore)(nil)
	_ Forgetter = (*PgStore)(nil)
)

// Curated reports whether a reviewer kept anything of the conversation for
// the dataset: a pair accepted or edited, a turn labelled or noted, or a
// re-run promoted. A discard is not keeping, and nor is a re-run nobody
// promoted (ADR-0059): retention may take those with their log.
func Curated(ctx context.Context, s Store, conversationID string) (bool, error) {
	ds, err := s.ForConversation(ctx, conversationID)
	if err != nil {
		return false, err
	}
	for _, d := range ds {
		if d.Status == StatusAccepted || d.Status == StatusEdited {
			return true, nil
		}
	}
	as, err := s.Annotations(ctx, conversationID)
	if err != nil || len(as) > 0 {
		return len(as) > 0, err
	}
	ps, err := s.Promotions(ctx, conversationID)
	return len(ps) > 0, err
}

// ConfirmedWakes are the seqs of a device log's rejections a reviewer
// confirmed as hard negatives (ADR-0058); a discarded one is not kept.
func ConfirmedWakes(ctx context.Context, s Store, conversationID string) (map[uint64]bool, error) {
	vs, err := s.WakeVerdicts(ctx, conversationID)
	if err != nil {
		return nil, err
	}
	out := map[uint64]bool{}
	for seq, v := range vs {
		if v.Status == WakeConfirmed {
			out[seq] = true
		}
	}
	return out, nil
}

// Forget removes every row of the conversation.
func (m *MemStore) Forget(_ context.Context, conversationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, d := range m.byPair {
		if d.ConversationID == conversationID {
			delete(m.byPair, id)
		}
	}
	for k := range m.annotations {
		if k.conversationID == conversationID {
			delete(m.annotations, k)
		}
	}
	for k := range m.promotions {
		if k.conversationID == conversationID {
			delete(m.promotions, k)
		}
	}
	for k := range m.wakes {
		if k.conversationID == conversationID {
			delete(m.wakes, k)
		}
	}
	m.reruns = slices.DeleteFunc(m.reruns, func(r Rerun) bool { return r.ConversationID == conversationID })
	return nil
}

// ForgetWakes removes the named verdicts.
func (m *MemStore) ForgetWakes(_ context.Context, conversationID string, seqs []uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, seq := range seqs {
		delete(m.wakes, turnKey{conversationID, seq})
	}
	return nil
}

// Forget removes every row of the conversation in one statement, so a
// failure leaves all of them.
func (p *PgStore) Forget(ctx context.Context, conversationID string) error {
	_, err := p.db.Exec(ctx, `
		WITH d AS (DELETE FROM curation_decisions WHERE conversation_id = $1),
		     a AS (DELETE FROM curation_annotations WHERE conversation_id = $1),
		     p AS (DELETE FROM curation_promotions WHERE conversation_id = $1),
		     r AS (DELETE FROM curation_reruns WHERE conversation_id = $1)
		DELETE FROM curation_wake_verdicts WHERE conversation_id = $1`, conversationID)
	if err != nil {
		return fmt.Errorf("forget curation of %s: %w", conversationID, err)
	}
	return nil
}

// ForgetWakes removes the named verdicts by primary key.
func (p *PgStore) ForgetWakes(ctx context.Context, conversationID string, seqs []uint64) error {
	keys := make([]int64, 0, len(seqs))
	for _, s := range seqs {
		keys = append(keys, int64(s))
	}
	if _, err := p.db.Exec(ctx,
		`DELETE FROM curation_wake_verdicts WHERE conversation_id = $1 AND event_seq = ANY($2)`,
		conversationID, keys); err != nil {
		return fmt.Errorf("forget wake verdicts of %s: %w", conversationID, err)
	}
	return nil
}
