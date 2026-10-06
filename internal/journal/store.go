package journal

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
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
	m.byConv[e.ConversationID] = append(log, detach(truncateClock(e)))
	return nil
}

// StoredClockResolution is what a Store preserves of Event.At. Postgres
// timestamptz is microsecond, so every backend truncates alike or replay
// depends on which one holds the log (SPEC §8).
const StoredClockResolution = time.Microsecond

func truncateClock(e Event) Event {
	e.At = e.At.Truncate(StoredClockResolution)
	return e
}

// Events returns a copy, so a reader cannot mutate the log.
func (m *MemStore) Events(_ context.Context, conversationID string) ([]Event, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	events := slices.Clone(m.byConv[conversationID])
	for i, e := range events {
		events[i] = detach(e)
	}
	return events, nil
}

// detach severs the Fields map an Event shares with its writer or reader.
// slices.Clone is shallow, so without this the log is not append-only.
func detach(e Event) Event {
	fields := make(map[string]string, len(e.Fields))
	maps.Copy(fields, e.Fields)
	e.Fields = fields
	return e
}

// LastSeq reports 0 for an unknown conversation.
func (m *MemStore) LastSeq(_ context.Context, conversationID string) (uint64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return uint64(len(m.byConv[conversationID])), nil
}
