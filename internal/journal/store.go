package journal

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// Store persists the log. PgStore is SPEC §8's Postgres JSONB, partitioned
// by conversation; `task test` stays hermetic against MemStore. Audio is kept
// apart, as files, by internal/blob.
type Store interface {
	// Append rejects a sequence number that is not exactly one past the last.
	Append(ctx context.Context, e Event) error
	Events(ctx context.Context, conversationID string) ([]Event, error)
	LastSeq(ctx context.Context, conversationID string) (uint64, error)
}

// Lister enumerates the log, for readers that start from no id: the review
// UI and the harvester's batch mode (SPEC §9.2).
type Lister interface {
	// Conversations orders by latest event, newest first, so recent work leads.
	Conversations(ctx context.Context) ([]string, error)
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

// Conversations breaks a tie on the latest clock by id, so the order is stable.
func (m *MemStore) Conversations(_ context.Context) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := slices.Collect(maps.Keys(m.byConv))
	latest := func(id string) time.Time {
		var at time.Time
		for _, e := range m.byConv[id] {
			if e.At.After(at) {
				at = e.At
			}
		}
		return at
	}
	slices.SortFunc(ids, func(a, b string) int {
		if c := latest(b).Compare(latest(a)); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	return ids, nil
}
