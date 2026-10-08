// Package curation persists a reviewer's verdicts on harvested preference
// candidates (SPEC §9.2). It lives beside the journal, not in it: the log
// records what the runtime did, a verdict records what a person later decided
// about it, and a verdict is revisable where the log is append-only.
package curation

import (
	"context"
	"fmt"
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
}

// MemStore is the in-memory Store used by tests and the demo server.
type MemStore struct {
	byPair map[string]Decision
	mu     sync.RWMutex
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{byPair: map[string]Decision{}}
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
