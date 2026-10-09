// Package memory keeps what each person in the household asked to be
// remembered, and recalls it into their conversations (SPEC §5, ADR-0040).
//
// It lives beside the journal, not in it, as curation verdicts do: the log
// records what the runtime did, including the call that remembered a fact,
// while forgetting has to make a memory stop being recalled, which an
// append-only log cannot.
package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// RecallLimit is how many memories a turn is given. Everything, newest
// first, until a household outgrows it and recall ranks by relevance.
const RecallLimit = 20

// What a turn is told of the person's earlier conversations: the few most
// recent from the past week, enough for "what did I ask yesterday" without
// crowding out the conversation in progress. A summary is kept a month,
// then pruned as newer ones are kept (SPEC §5, ADR-0042).
const (
	SummaryLimit  = 5
	SummaryWindow = 7 * 24 * time.Hour
	SummaryKeep   = 30 * 24 * time.Hour
)

// Memory is one remembered fact and where it was said.
type Memory struct {
	ID        string
	Person    string
	Fact      string
	Shareable bool

	// ConversationID and CallID are the remember call that made it, so the
	// log's record of what was said can be found from the memory.
	ConversationID string
	CallID         string
	At             time.Time
}

// Recalled is the memory as the model is told it.
func (m Memory) Recalled() journal.Memory {
	return journal.Memory{ID: m.ID, Person: m.Person, Fact: m.Fact, Shareable: m.Shareable}
}

func (m Memory) validate() error {
	switch {
	case m.ID == "":
		return fmt.Errorf("memory: no id")
	case m.Person == "":
		return fmt.Errorf("memory %s: nobody it is about", m.ID)
	case strings.TrimSpace(m.Fact) == "":
		return fmt.Errorf("memory %s: nothing to remember", m.ID)
	}
	return nil
}

// Summary is what one conversation was about, kept for one person who was in
// it. Private to them: everyone it is kept for was there.
type Summary struct {
	ConversationID string
	Person         string
	Text           string

	// At is when the conversation last heard anyone: what "yesterday" is
	// measured against.
	At time.Time
}

// Recalled is the summary as the model is told it.
func (s Summary) Recalled() journal.Summary {
	return journal.Summary{ConversationID: s.ConversationID, At: s.At, Text: s.Text}
}

func (s Summary) validate() error {
	switch {
	case s.ConversationID == "":
		return fmt.Errorf("summary: no conversation")
	case s.Person == "":
		return fmt.Errorf("summary of %s: nobody it is kept for", s.ConversationID)
	case strings.TrimSpace(s.Text) == "":
		return fmt.Errorf("summary of %s: nothing to keep", s.ConversationID)
	}
	return nil
}

// Store persists memories. Postgres is the real backend; MemStore keeps
// `task test` hermetic, as with the journal.
type Store interface {
	Remember(ctx context.Context, m Memory) error

	// Forget deletes one of the person's own memories, and reports whether
	// they had one by that id. Another person's memory is not theirs to
	// forget, shared or not.
	Forget(ctx context.Context, person, id string) (bool, error)

	// Recall returns the person's memories and the ones others shared, newest
	// first, at most limit.
	Recall(ctx context.Context, person string, limit int) ([]Memory, error)

	// Summarized keeps a conversation's summary for one person. A newer
	// summary of the same conversation, which a resumed conversation that
	// ended again writes, replaces it; an older one does not. Keeping one
	// prunes that person's summaries older than SummaryKeep before it.
	Summarized(ctx context.Context, s Summary) error

	// Summaries returns the person's summaries at or after since, other than
	// the conversation named, newest first, at most limit.
	Summaries(ctx context.Context, person, except string, since time.Time, limit int) ([]Summary, error)
}

// visible reports whether a memory may be recalled to person.
func visible(m Memory, person string) bool { return m.Person == person || m.Shareable }

// newestFirst orders by when a memory was made, then by id, so two made in
// the same microsecond recall in the same order from every backend.
func newestFirst(a, b Memory) int {
	if c := b.At.Compare(a.At); c != 0 {
		return c
	}
	return cmp.Compare(a.ID, b.ID)
}

// MemStore is the in-memory Store used by tests.
type MemStore struct {
	mu        sync.Mutex
	byID      map[string]Memory
	summaries map[summaryKey]Summary
}

type summaryKey struct{ conversation, person string }

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{byID: map[string]Memory{}, summaries: map[summaryKey]Summary{}}
}

// Remember stores m, refusing an id already taken.
func (s *MemStore) Remember(_ context.Context, m Memory) error {
	if err := m.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.byID[m.ID]; taken {
		return fmt.Errorf("memory %s: id already taken", m.ID)
	}
	m.At = m.At.Truncate(journal.StoredClockResolution).UTC()
	s.byID[m.ID] = m
	return nil
}

// Forget deletes the person's memory by id.
func (s *MemStore) Forget(_ context.Context, person, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.byID[id]
	if !ok || m.Person != person {
		return false, nil
	}
	delete(s.byID, id)
	return true, nil
}

// Recall returns what person may be told, newest first.
func (s *MemStore) Recall(_ context.Context, person string, limit int) ([]Memory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Memory
	for _, m := range s.byID {
		if visible(m, person) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, newestFirst)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Summarized keeps s unless a newer summary of the conversation is kept.
func (s *MemStore) Summarized(_ context.Context, sum Summary) error {
	if err := sum.validate(); err != nil {
		return err
	}
	sum.At = sum.At.Truncate(journal.StoredClockResolution).UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, old := range s.summaries {
		if k.person == sum.Person && old.At.Before(sum.At.Add(-SummaryKeep)) {
			delete(s.summaries, k)
		}
	}
	k := summaryKey{sum.ConversationID, sum.Person}
	if old, ok := s.summaries[k]; !ok || !old.At.After(sum.At) {
		s.summaries[k] = sum
	}
	return nil
}

// Summaries returns the person's recent summaries, newest first.
func (s *MemStore) Summaries(_ context.Context, person, except string, since time.Time, limit int) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Summary
	for k, sum := range s.summaries {
		if k.person == person && k.conversation != except && !sum.At.Before(since) {
			out = append(out, sum)
		}
	}
	slices.SortFunc(out, func(a, b Summary) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return cmp.Compare(a.ConversationID, b.ConversationID)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Recaller is a Store as the session asks it: a guest recalls nothing, and
// anyone else at most RecallLimit memories and SummaryLimit conversations
// from the past SummaryWindow.
func Recaller(s Store) session.Memories { return recaller{s} }

type recaller struct{ s Store }

func (r recaller) Recall(ctx context.Context, person, conversationID string, now time.Time) (session.Recollection, error) {
	var out session.Recollection
	if person == "" {
		return out, nil
	}
	ms, err := r.s.Recall(ctx, person, RecallLimit)
	if err != nil {
		return out, fmt.Errorf("recall %s: %w", person, err)
	}
	for _, m := range ms {
		out.Memories = append(out.Memories, m.Recalled())
	}
	ss, err := r.s.Summaries(ctx, person, conversationID, now.Add(-SummaryWindow), SummaryLimit)
	if err != nil {
		return out, fmt.Errorf("recall %s's conversations: %w", person, err)
	}
	for _, s := range ss {
		out.Summaries = append(out.Summaries, s.Recalled())
	}
	return out, nil
}

// Keep stores the summary for each person; a guest is nobody to keep it for.
func (r recaller) Keep(ctx context.Context, people []string, s journal.Summary) error {
	for _, p := range people {
		if p == "" {
			continue
		}
		err := r.s.Summarized(ctx, Summary{ConversationID: s.ConversationID, Person: p, Text: s.Text, At: s.At})
		if err != nil {
			return fmt.Errorf("keep %s's summary of %s: %w", p, s.ConversationID, err)
		}
	}
	return nil
}
