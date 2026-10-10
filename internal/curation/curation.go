// Package curation persists what a reviewer decides (SPEC §9.2): verdicts on
// preference candidates, labels on turns, the re-runs they asked, re-run
// takes promoted to a pair's chosen side, and the word on rejected wakes
// (SPEC §9.3). It lives beside the journal, not in it: the log records what
// the runtime did, a verdict records what a person later decided about it,
// and a verdict is revisable where the log is append-only.
package curation

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Status is where the Curate flow left a pair. Unreviewed is the absence of
// a decision, so it is not a value a store will accept.
type Status string

const (
	StatusEdited    Status = "edited"
	StatusAccepted  Status = "accepted"
	StatusDiscarded Status = "discarded"
)

// Decision is one reviewer verdict on one harvested pair, keyed by the
// harvester's pair id (conversation/cut-seq). The last write wins.
type Decision struct {
	PairID         string
	ConversationID string
	Status         Status

	// Chosen is the side the reviewer settled on. Empty for a discard.
	Chosen string

	// Unfixed marks an accept that overrode the mismatch guard; an export
	// must skip it until the chosen side answers the shared prompt.
	Unfixed bool

	// Reason is why a discarded pair left the pool.
	Reason string

	// Prev is where the pair sat before this verdict, so Undo works across
	// requests. Empty or "unreviewed" both mean a first verdict.
	Prev Status

	DecidedAt time.Time
}

func (d Decision) validate() error {
	if d.PairID == "" {
		return fmt.Errorf("curation: decision without a pair id")
	}
	if d.ConversationID == "" {
		return fmt.Errorf("curation: decision %s without a conversation", d.PairID)
	}
	switch d.Prev {
	case "", "unreviewed", StatusEdited, StatusAccepted, StatusDiscarded:
	default:
		return fmt.Errorf("curation: decision %s with prev %q", d.PairID, d.Prev)
	}
	switch d.Status {
	case StatusEdited, StatusAccepted, StatusDiscarded:
		return nil
	}
	return fmt.Errorf("curation: decision %s with status %q", d.PairID, d.Status)
}

// Store persists decisions. Postgres is the real backend; MemStore keeps
// `task test` hermetic, as with the journal.
type Store interface {
	// Put stores or replaces the decision on its pair.
	Put(ctx context.Context, d Decision) error
	Get(ctx context.Context, pairID string) (Decision, bool, error)
	// Delete returns a pair to unreviewed; deleting an absent pair is a no-op.
	Delete(ctx context.Context, pairID string) error
	ForConversation(ctx context.Context, conversationID string) (map[string]Decision, error)

	// PutAnnotation stores or replaces a turn's annotation; an empty one
	// returns the turn to unannotated.
	PutAnnotation(ctx context.Context, a Annotation) error
	// Annotations returns the conversation's annotations keyed by turn seq.
	Annotations(ctx context.Context, conversationID string) (map[uint64]Annotation, error)

	// PutPromotion stores or replaces the turn's promoted take.
	PutPromotion(ctx context.Context, p Promotion) error
	// Promotions returns the conversation's promoted takes keyed by turn seq.
	Promotions(ctx context.Context, conversationID string) (map[uint64]Promotion, error)

	// AddRerun keeps a re-run and returns the id the store gave it.
	AddRerun(ctx context.Context, r Rerun) (uint64, error)
	// Reruns returns the conversation's re-runs, newest first.
	Reruns(ctx context.Context, conversationID string) ([]Rerun, error)
	// PutWakeVerdict stores or replaces the word on a rejected wake; an
	// empty status returns it to unreviewed.
	PutWakeVerdict(ctx context.Context, v WakeVerdict) error
	// WakeVerdicts returns a device log's verdicts keyed by event seq.
	WakeVerdicts(ctx context.Context, conversationID string) (map[uint64]WakeVerdict, error)
}

// MemStore is the in-memory Store used by tests and the demo server.
type MemStore struct {
	byPair      map[string]Decision
	annotations map[turnKey]Annotation
	promotions  map[turnKey]Promotion
	reruns      []Rerun // in the order added, which is the id's
	wakes       map[turnKey]WakeVerdict
	mu          sync.RWMutex
}

// turnKey names one turn of one conversation.
type turnKey struct {
	conversationID string
	seq            uint64
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{
		byPair:      map[string]Decision{},
		annotations: map[turnKey]Annotation{},
		promotions:  map[turnKey]Promotion{},
		wakes:       map[turnKey]WakeVerdict{},
	}
}

// Put replaces any earlier decision on the pair.
func (m *MemStore) Put(_ context.Context, d Decision) error {
	if err := d.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.byPair[d.PairID] = truncateClock(d)
	return nil
}

// StoredClockResolution is what a Store preserves of DecidedAt, matching the
// journal: Postgres timestamptz is microsecond, so every backend truncates.
const StoredClockResolution = time.Microsecond

func truncateClock(d Decision) Decision {
	d.DecidedAt = d.DecidedAt.Truncate(StoredClockResolution)
	return d
}

// Delete returns the pair to unreviewed.
func (m *MemStore) Delete(_ context.Context, pairID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byPair, pairID)
	return nil
}

// Get reports ok false for a pair nobody has reviewed.
func (m *MemStore) Get(_ context.Context, pairID string) (Decision, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.byPair[pairID]
	return d, ok, nil
}

// ForConversation returns the conversation's decisions keyed by pair id.
func (m *MemStore) ForConversation(_ context.Context, conversationID string) (map[string]Decision, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[string]Decision{}
	for id, d := range m.byPair {
		if d.ConversationID == conversationID {
			out[id] = d
		}
	}
	return out, nil
}

// PutAnnotation replaces the turn's annotation, or deletes an empty one.
func (m *MemStore) PutAnnotation(_ context.Context, a Annotation) error {
	if err := a.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := turnKey{a.ConversationID, a.Seq}
	if a.Empty() {
		delete(m.annotations, k)
		return nil
	}
	a.Labels = slices.Clone(a.Labels)
	if len(a.Labels) == 0 {
		a.Labels = nil // as Postgres reads back an empty array
	}
	a.AnnotatedAt = a.AnnotatedAt.Truncate(StoredClockResolution)
	m.annotations[k] = a
	return nil
}

// Annotations returns the conversation's annotations keyed by turn seq.
func (m *MemStore) Annotations(_ context.Context, conversationID string) (map[uint64]Annotation, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[uint64]Annotation{}
	for k, a := range m.annotations {
		if k.conversationID == conversationID {
			a.Labels = slices.Clone(a.Labels)
			out[k.seq] = a
		}
	}
	return out, nil
}

// PutPromotion replaces the turn's promoted take.
func (m *MemStore) PutPromotion(_ context.Context, p Promotion) error {
	if err := p.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p.Calls = slices.Clone(p.Calls)
	if len(p.Calls) == 0 {
		p.Calls = nil // as Postgres reads back an empty list
	}
	p.PromotedAt = p.PromotedAt.Truncate(StoredClockResolution)
	m.promotions[turnKey{p.ConversationID, p.Seq}] = p
	return nil
}

// Promotions returns the conversation's promoted takes keyed by turn seq.
func (m *MemStore) Promotions(_ context.Context, conversationID string) (map[uint64]Promotion, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[uint64]Promotion{}
	for k, p := range m.promotions {
		if k.conversationID == conversationID {
			p.Calls = slices.Clone(p.Calls)
			out[k.seq] = p
		}
	}
	return out, nil
}

// AddRerun keeps the re-run under the next id.
func (m *MemStore) AddRerun(_ context.Context, r Rerun) (uint64, error) {
	if err := r.validate(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	r.ID = uint64(len(m.reruns) + 1)
	r.Takes = cloneTakes(r.Takes)
	r.RanAt = r.RanAt.Truncate(StoredClockResolution)
	m.reruns = append(m.reruns, r)
	return r.ID, nil
}

// Reruns returns the conversation's re-runs, newest first.
func (m *MemStore) Reruns(_ context.Context, conversationID string) ([]Rerun, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []Rerun
	for _, r := range m.reruns {
		if r.ConversationID == conversationID {
			r.Takes = cloneTakes(r.Takes)
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, b Rerun) int {
		if c := b.RanAt.Compare(a.RanAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	return out, nil
}

// PutWakeVerdict replaces the rejection's verdict, or deletes an empty one.
func (m *MemStore) PutWakeVerdict(_ context.Context, v WakeVerdict) error {
	if err := v.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := turnKey{v.ConversationID, v.Seq}
	if v.Status == "" {
		delete(m.wakes, k)
		return nil
	}
	v.JudgedAt = v.JudgedAt.Truncate(StoredClockResolution)
	m.wakes[k] = v
	return nil
}

// WakeVerdicts returns a device log's verdicts keyed by event seq.
func (m *MemStore) WakeVerdicts(_ context.Context, conversationID string) (map[uint64]WakeVerdict, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := map[uint64]WakeVerdict{}
	for k, v := range m.wakes {
		if k.conversationID == conversationID {
			out[k.seq] = v
		}
	}
	return out, nil
}
