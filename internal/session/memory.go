package session

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
)

// Memories is what the model is told it remembers about a person, and where
// a finished conversation is kept for the people who were in it (SPEC §5).
type Memories interface {
	// Recall returns the person's own memories and what the rest of the
	// household shared, and the person's recent conversations other than
	// this one, as of when the ask was heard.
	Recall(ctx context.Context, a Ask) (Recollection, error)

	// Keep stores what a conversation was about for each of the people in
	// it, replacing an older summary of the same conversation.
	Keep(ctx context.Context, people []string, s journal.Summary) error
}

// Ask is the turn a recollection is for.
type Ask struct {
	// Person is the identified speaker; a guest recalls nothing.
	Person         string
	ConversationID string
	// Now is when the turn was heard, and Words what was said in it: what
	// the memories most worth telling are relevant to.
	Now   time.Time
	Words string
}

// Recollection is what one turn is told it remembers.
type Recollection struct {
	Memories  []journal.Memory
	Summaries []journal.Summary

	// RankedBy names the embedding model that chose them by relevance to
	// the words, or is empty when they are the newest (ADR-0044).
	RankedBy string
}

// Summarizer writes what a finished conversation was about, for the people
// in it to be told in their later ones (SPEC §5).
type Summarizer interface {
	Summarize(ctx context.Context, dialogue []journal.Entry, people []string) (string, error)
}

// Caller is who a tool call is made for, which a person-scoped executor needs
// and the arguments must not carry: a model that names the person could name
// anyone (SPEC §5).
type Caller struct {
	// Person is the identified speaker, empty for a guest.
	Person         string
	ConversationID string
	CallID         string

	// Satellite is the device the call was made on: where a timer it sets
	// goes off, and the room an announcement is not repeated in.
	Satellite string
}

type callerKey struct{}

// CallerFrom reports who the call running under ctx was made for.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey{}).(Caller)
	return c, ok
}

// WithCaller is how the session hands an executor its caller. Exported for
// the executors' own tests, which run without a session.
func WithCaller(ctx context.Context, c Caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// denied reports whether a call is refused for who is asking: a person-scoped
// tool needs an identified speaker, unless it declares that a guest may use
// it too (SPEC §5).
func denied(spec registry.ToolSpec, person string) bool {
	return spec.Scope == registry.ScopePerson && person == "" && spec.UnknownSpeaker != "guest_fallback"
}

// recall records what the model will be told it remembers, when that differs
// from what the log already says it was told, and returns the state with it.
// A guest recalls nothing. Recorded rather than fetched per ask, so a replay
// asks with exactly what this turn was given, as of when it was heard.
//
// A guest who follows someone the log says was told something is recorded
// as told nothing, or the guest's turn would inherit that person's memories
// from the log (ADR-0048).
func (s *Session) recall(ctx context.Context, st journal.State, words string) (journal.State, error) {
	if s.sup.cfg.Memories == nil {
		return st, nil
	}
	var got Recollection
	if st.Speaker != "" {
		var err error
		got, err = s.sup.cfg.Memories.Recall(ctx, Ask{
			Person: st.Speaker, ConversationID: s.convID, Now: st.HeardAt, Words: words,
		})
		if err != nil {
			return st, err
		}
	}
	if st.RecalledFor == st.Speaker && slices.Equal(got.Memories, st.Recalled) &&
		slices.EqualFunc(got.Summaries, st.RecalledSummaries, sameSummary) &&
		got.RankedBy == st.RecalledRankedBy {
		return st, nil
	}
	fields := map[string]string{
		"person":         st.Speaker,
		"memories_json":  journal.EncodeMemories(got.Memories),
		"summaries_json": journal.EncodeSummaries(got.Summaries),
	}
	if got.RankedBy != "" {
		fields["ranked_by"] = got.RankedBy
	}
	if err := s.record(journal.Record{Kind: journal.KindMemoryRecalled, Fields: fields}); err != nil {
		return st, err
	}
	return s.State(ctx)
}

// sameSummary compares instants, not locations: the log hands back in UTC
// what the store may have handed over in another zone.
func sameSummary(a, b journal.Summary) bool {
	return a.ConversationID == b.ConversationID && a.Text == b.Text && a.At.Equal(b.At)
}

// startSummary snapshots the conversation as it ended and summarizes it in
// the background. The snapshot is taken here, not in the background: a
// person back within the migration window resumes the same log, and a
// summary read after that would describe a conversation still going, racing
// the one its own end starts (ADR-0043).
//
// Nothing is summarized for a conversation with nobody identified in it, or
// nothing said: there is nobody to keep it for, or nothing to keep.
func (s *Session) startSummary() {
	st, err := s.State(context.WithoutCancel(s.ctx))
	if err != nil {
		s.sup.cfg.Log.Warn("summarize: replay", "conversation", s.convID, "err", err)
		return
	}
	if len(st.Participants) == 0 || !slices.ContainsFunc(st.Dialogue, heard) {
		return
	}
	s.sup.cfg.Summarizing.Add(1)
	go s.summarize(st)
}

// summarize writes what the conversation was about and keeps it for each
// identified person who was in it. It runs after the session has closed,
// bounded by SummaryTimeout: the people have walked away, and nobody is
// waiting on it. Whether it worked is recorded either way, so a missing
// summary is in the log rather than silently absent.
func (s *Session) summarize(st journal.State) {
	defer s.sup.cfg.Summarizing.Done()
	ctx, cancel := context.WithCancel(context.WithoutCancel(s.ctx))
	defer cancel()
	timeout := s.sup.cfg.Timers.After(s.sup.cfg.SummaryTimeout)
	go func() {
		select {
		case <-timeout:
			cancel()
		case <-ctx.Done():
		}
	}()

	people, err := json.Marshal(st.Participants)
	if err != nil {
		// A slice of strings cannot fail to encode.
		panic(fmt.Sprintf("encode participants: %v", err))
	}
	fields := map[string]string{"people_json": string(people)}
	text, err := s.sup.cfg.Summarizer.Summarize(ctx, st.Dialogue, st.Participants)
	text = strings.TrimSpace(text)
	switch {
	case err != nil:
		fields["error"] = err.Error()
	case text == "":
		fields["error"] = "the model wrote nothing"
	default:
		fields["summary"] = text
		sum := journal.Summary{ConversationID: s.convID, At: st.HeardAt, Text: text}
		if err := s.sup.cfg.Memories.Keep(ctx, st.Participants, sum); err != nil {
			fields["error"] = "keep: " + err.Error()
		}
	}
	if err := s.record(journal.Record{Kind: journal.KindConversationSummarized, Fields: fields}); err != nil {
		s.sup.cfg.Log.Warn("summarize: record", "conversation", s.convID, "err", err)
	}
}

func heard(e journal.Entry) bool { return e.Kind == journal.EntryHeard }
