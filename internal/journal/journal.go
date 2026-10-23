// Package journal is the append-only event log the orchestrator reduces over.
// It is the runtime's source of truth, not instrumentation (SPEC §8).
package journal

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"
)

// Versions pins the configuration an event was produced under. Without it a
// training pair cannot be attributed and replay cannot reproduce a completion.
type Versions struct {
	Model      string
	Prompt     string
	ToolSchema string

	// STT names the recogniser behind every transcript; TTS the voice behind
	// every utterance, as model/voice (ADR-0032). Neither gates a completion:
	// complete() attributes a pair to its model, not to what heard or spoke.
	STT string
	TTS string
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

	// Speculative marks work that may be discarded, which replay must tell
	// from committed work (SPEC §11). A kind declared speculative needs it.
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

	// seq serialises the read-then-write of a sequence number. Here and not in
	// the caller because the gap is Append's own, and the log has more than one
	// writer by design: the same person waking a second satellite resumes the
	// same conversation (SPEC §4.5), and a lock held by one of those sessions
	// does not cover the other.
	seq seqLocks

	observers []Observer
}

// Observer hears the outcome of each append once the store has answered.
// It derives from the log and never writes to it, so the log stays the one
// source of truth (ADR-0007, ADR-0068). It runs on the writer's goroutine,
// so it must not block.
type Observer interface {
	// Appended is an event as stamped and stored.
	Appended(Event)
	// AppendFailed is a record that did not reach the log.
	AppendFailed(Kind, error)
}

// Observe makes o hear every append from now on, beside any observer
// already added. Call it before the journal is shared.
func (j *Journal) Observe(o Observer) { j.observers = append(j.observers, o) }

// seqLocks hands out one lock per conversation.
//
// Per conversation rather than one for the process: both halves of the
// read-then-write are store round trips, and a session records on a context
// with no cancellation or deadline (internal/session/session.go), so under a
// shared lock one hung connection would stop every conversation in the
// household from recording anything at all.
type seqLocks struct {
	mu   sync.Mutex
	held map[string]*seqLock
}

// seqLock is one conversation's lock and the number of writers holding or
// waiting for it. Counted so the entry can go when the last one leaves:
// conversation ids are never reused, so a map that only grows keeps a mutex
// per conversation for as long as the process lives.
type seqLock struct {
	mu   sync.Mutex
	refs int
}

// lock blocks until this conversation's turn and returns its release.
func (l *seqLocks) lock(conversationID string) func() {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*seqLock{}
	}
	e := l.held[conversationID]
	if e == nil {
		e = &seqLock{}
		l.held[conversationID] = e
	}
	// Counted before the wait, not after. An entry dropped while this writer is
	// still queued would leave it waiting on a lock the map no longer names,
	// and hand the next writer a fresh one that serialises nothing.
	e.refs++
	l.mu.Unlock()

	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		l.mu.Lock()
		defer l.mu.Unlock()
		e.refs--
		if e.refs == 0 {
			delete(l.held, conversationID)
		}
	}
}

// New binds a journal to a store, clock, and the versions currently in effect.
func New(store Store, clock Clock, v Versions) *Journal {
	return &Journal{store: store, clock: clock, versions: v}
}

// Append validates r against the generated taxonomy and writes it.
func (j *Journal) Append(ctx context.Context, conversationID string, r Record) (Event, error) {
	e, err := j.append(ctx, conversationID, r)
	for _, o := range j.observers {
		if err != nil {
			o.AppendFailed(r.Kind, err)
		} else {
			o.Appended(e)
		}
	}
	return e, err
}

func (j *Journal) append(ctx context.Context, conversationID string, r Record) (Event, error) {
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
	if meta.Speculative && !r.Speculative {
		// Written as committed, replay would fold work nobody heard (SPEC §11).
		return Event{}, fmt.Errorf("append %s: a speculative kind must be written as speculative", r.Kind)
	}
	if meta.HasAudio && r.AudioRef == "" {
		return Event{}, fmt.Errorf("append %s: audio-bearing event needs a blob reference", r.Kind)
	}
	if meta.RequiresVersions && !j.versions.complete() {
		return Event{}, fmt.Errorf("append %s: versions incomplete, pair cannot be attributed", r.Kind)
	}

	defer j.seq.lock(conversationID)()

	// The lock orders this process's writers only. Another process's write,
	// such as the review UI's, can take the seq in between: go after it.
	var err error
	for range appendAttempts {
		var seq uint64
		seq, err = j.store.LastSeq(ctx, conversationID)
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
		if err = j.store.Append(ctx, e); err == nil {
			return e, nil
		}
		if !errors.Is(err, ErrSeqTaken) {
			break
		}
	}
	return Event{}, fmt.Errorf("append %s: %w", r.Kind, err)
}

// appendAttempts bounds how often a write goes after another process's
// before it gives up: one other writer loses it a seq once, not forever.
const appendAttempts = 5

// Events returns one conversation's log in sequence order.
func (j *Journal) Events(ctx context.Context, conversationID string) ([]Event, error) {
	return j.store.Events(ctx, conversationID)
}
