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

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// RecallLimit is how many memories a turn is given: everything, until a
// household outgrows it and recall chooses by relevance (ADR-0044).
const RecallLimit = 20

// What a turn is told of the person's earlier conversations: the few most
// recent from the past week, enough for "what did I ask yesterday" without
// crowding out the conversation in progress. A summary is kept a month,
// then pruned as newer ones are kept (SPEC §5, ADR-0043).
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
// anyone else at most RecallLimit memories and SummaryLimit conversations.
// Without an Embedder they are the newest, the conversations from the past
// SummaryWindow; with one, any of the past SummaryKeep's can be chosen, by
// relevance to what was just said as well as by recency (ADR-0044).
func Recaller(s Store, cfg RecallConfig) session.Memories {
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultRankTimeout
	}
	r := recaller{s: s, cfg: cfg}
	if cfg.Embedder != nil {
		r.rank = &ranker{e: cfg.Embedder, cache: &vectors{}}
	}
	return r
}

type recaller struct {
	s    Store
	cfg  RecallConfig
	rank *ranker
}

func (r recaller) Recall(ctx context.Context, a session.Ask) (session.Recollection, error) {
	var out session.Recollection
	if a.Person == "" {
		return out, nil
	}
	pool, since := RecallLimit, a.Now.Add(-SummaryWindow)
	if r.rank != nil {
		pool, since = rankPool, a.Now.Add(-SummaryKeep)
	}
	ms, err := r.s.Recall(ctx, a.Person, pool)
	if err != nil {
		return out, fmt.Errorf("recall %s: %w", a.Person, err)
	}
	ss, err := r.s.Summaries(ctx, a.Person, a.ConversationID, since, pool)
	if err != nil {
		return out, fmt.Errorf("recall %s's conversations: %w", a.Person, err)
	}
	ms, ss, out.RankedBy = r.choose(ctx, a, ms, ss)
	for _, m := range ms {
		out.Memories = append(out.Memories, m.Recalled())
	}
	for _, s := range ss {
		out.Summaries = append(out.Summaries, s.Recalled())
	}
	return out, nil
}

// choose cuts the candidates to what a turn is told: by relevance where
// there are more than fit and an Embedder to rank them, otherwise the
// newest. Ranking that fails costs the turn its relevance, never its
// memories: it is told what it would have been without an Embedder.
func (r recaller) choose(ctx context.Context, a session.Ask, ms []Memory, ss []Summary) ([]Memory, []Summary, string) {
	if r.rank == nil || strings.TrimSpace(a.Words) == "" {
		return newest(ms, ss, a.Now)
	}
	if len(ms) <= RecallLimit && len(ss) <= SummaryLimit {
		// Everything fits: there is nothing to choose between.
		return ms, ss, ""
	}
	ctx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()
	ranked, err := r.ranked(ctx, a.Words, ms, ss)
	if err != nil {
		if r.cfg.Failed != nil {
			r.cfg.Failed(a.Person, err)
		}
		return newest(ms, ss, a.Now)
	}
	return ranked.ms, ranked.ss, r.rank.e.EmbedModel()
}

type chosen struct {
	ms []Memory
	ss []Summary
}

// ranked embeds every pool that overflows in one warm and the words once,
// then chooses each pool's best by itself: a busy person's memories and
// conversations cost one wait, not two.
func (r recaller) ranked(ctx context.Context, words string, ms []Memory, ss []Summary) (chosen, error) {
	out := chosen{ms: ms, ss: ss}
	var texts []string
	if len(ms) > RecallLimit {
		for _, m := range ms {
			texts = append(texts, m.Fact)
		}
	}
	nm := len(texts)
	if len(ss) > SummaryLimit {
		for _, s := range ss {
			texts = append(texts, s.Text)
		}
	}
	q, vecs, err := r.rank.vectors(ctx, words, texts)
	if err != nil {
		return out, fmt.Errorf("rank: %w", err)
	}
	if nm > 0 {
		out.ms = pick(ms, fuse(q, vecs[:nm], RecallLimit))
	}
	if len(vecs) > nm {
		out.ss = pick(ss, fuse(q, vecs[nm:], SummaryLimit))
	}
	return out, nil
}

// newest is what a turn is told without ranking: the newest memories, and
// the newest conversations of the past SummaryWindow.
func newest(ms []Memory, ss []Summary, now time.Time) ([]Memory, []Summary, string) {
	ms = ms[:min(len(ms), RecallLimit)]
	recent := ss[:0:0]
	for _, s := range ss {
		if !s.At.Before(now.Add(-SummaryWindow)) && len(recent) < SummaryLimit {
			recent = append(recent, s)
		}
	}
	return ms, recent, ""
}

func pick[T any](from []T, at []int) []T {
	out := make([]T, len(at))
	for i, j := range at {
		out[i] = from[j]
	}
	return out
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
