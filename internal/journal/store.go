package journal

import (
	"context"
	"fmt"
	"slices"
	"sync"
)

// Store persists the log. SPEC §8 names Postgres JSONB partitioned by
// conversation plus MinIO for blobs; that implementation lands later, and
// `task test` stays hermetic against MemStore.
type Store interface {
	// Append rejects a sequence number that is not exactly one past the last.
	Append(ctx context.Context, e Event) error
	Events(ctx context.Context, conversationID string) ([]Event, error)
	LastSeq(ctx context.Context, conversationID string) (uint64, error)
}

// MemStore is the in-memory Store used by tests and the replay harness.
type MemStore struct {
	byConv map[string][]Event
	mu     sync.RWMutex
}

// NewMemStore returns an empty in-memory store.
func NewMemStore() *MemStore {
	return &MemStore{byConv: map[string][]Event{}}
}

// Append enforces the monotonic, gapless sequence the reducer relies on.
func (m *MemStore) Append(_ context.Context, e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	log := m.byConv[e.ConversationID]
	if want := uint64(len(log)) + 1; e.Seq != want {
		return fmt.Errorf("seq %d for %s: want %d", e.Seq, e.ConversationID, want)
	}
	m.byConv[e.ConversationID] = append(log, e)
	return nil
}

// Events returns a copy, so a reader cannot mutate the log.
func (m *MemStore) Events(_ context.Context, conversationID string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return slices.Clone(m.byConv[conversationID]), nil
}

// LastSeq reports 0 for an unknown conversation.
func (m *MemStore) LastSeq(_ context.Context, conversationID string) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return uint64(len(m.byConv[conversationID])), nil
}
