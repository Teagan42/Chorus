// Package journal is the append-only event log the orchestrator reduces over.
// It is the runtime's source of truth, not instrumentation (SPEC §8).
package journal

import (
	"context"
	"fmt"
	"maps"
	"time"
)

// Versions pins the configuration an event was produced under. Without it a
// training pair cannot be attributed and replay cannot reproduce a completion.
type Versions struct {
	Model      string
	Prompt     string
	ToolSchema string
}

func (v Versions) complete() bool {
	return v.Model != "" && v.Prompt != "" && v.ToolSchema != ""
}

// Clock is injected everywhere; code reading time.Now directly is untestable.
type Clock interface {
	Now() time.Time
}

// FixedClock returns a Clock stuck at t.
func FixedClock(t time.Time) Clock { return fixed{t} }

type fixed struct{ t time.Time }

func (f fixed) Now() time.Time { return f.t }

// Record is a caller's event before the journal stamps it.
type Record struct {
	Fields map[string]string
	Kind   Kind

	// AudioRef points at the blob store; audio is never inlined.
	AudioRef string

	// Speculative marks work that may be discarded. Phase 1 writes none, but
	// replay must be able to tell it from committed work (SPEC §11).
	Speculative bool
}

// Event is a stamped, immutable log entry.
type Event struct {
	At             time.Time
	Fields         map[string]string
	ConversationID string
	AudioRef       string
	Kind           Kind
	Actor          Actor
	Versions       Versions
	Seq            uint64
	Speculative    bool
}

// Journal stamps and validates every write.
type Journal struct {
	store    Store
	clock    Clock
	versions Versions
}

// New binds a journal to a store, clock, and the versions currently in effect.
func New(store Store, clock Clock, v Versions) *Journal {
	return &Journal{store: store, clock: clock, versions: v}
}

// Append validates r against the generated taxonomy and writes it.
func (j *Journal) Append(ctx context.Context, conversationID string, r Record) (Event, error) {
	meta, ok := Meta[r.Kind]
	if !ok {
		return Event{}, fmt.Errorf("append %q: undeclared event kind", r.Kind)
	}
	if conversationID == "" {
		return Event{}, fmt.Errorf("append %s: empty conversation id", r.Kind)
	}
	for _, f := range meta.RequiredFields {
		if r.Fields[f] == "" {
			return Event{}, fmt.Errorf("append %s: missing required field %q", r.Kind, f)
		}
	}
	if meta.HasAudio && r.AudioRef == "" {
		return Event{}, fmt.Errorf("append %s: audio-bearing event needs a blob reference", r.Kind)
	}
	if meta.RequiresVersions && !j.versions.complete() {
		return Event{}, fmt.Errorf("append %s: versions incomplete, pair cannot be attributed", r.Kind)
	}

	seq, err := j.store.LastSeq(ctx, conversationID)
	if err != nil {
		return Event{}, fmt.Errorf("last seq %s: %w", conversationID, err)
	}
	e := Event{
		Seq:            seq + 1,
		ConversationID: conversationID,
		Kind:           r.Kind,
		Actor:          meta.Actor,
		At:             j.clock.Now(),
		Speculative:    r.Speculative,
		Versions:       j.versions,
		AudioRef:       r.AudioRef,
		Fields:         maps.Clone(r.Fields),
	}
	if err := j.store.Append(ctx, e); err != nil {
		return Event{}, fmt.Errorf("append %s: %w", r.Kind, err)
	}
	return e, nil
}

// Events returns one conversation's log in sequence order.
func (j *Journal) Events(ctx context.Context, conversationID string) ([]Event, error) {
	return j.store.Events(ctx, conversationID)
}
