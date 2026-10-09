package session

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/registry"
)

// Memories is what the model is told it remembers about a person, and where
// a finished conversation is kept for the people who were in it (SPEC §5).
type Memories interface {
	// Recall returns the person's own memories and what the rest of the
	// household shared, and the person's recent conversations other than
	// this one, as of now.
	Recall(ctx context.Context, person, conversationID string, now time.Time) (Recollection, error)

	// Keep stores what a conversation was about for each of the people in
	// it, replacing an older summary of the same conversation.
	Keep(ctx context.Context, people []string, s journal.Summary) error
}

// Recollection is what one turn is told it remembers.
type Recollection struct {
	Memories  []journal.Memory
	Summaries []journal.Summary
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
func (s *Session) recall(ctx context.Context, st journal.State) (journal.State, error) {
	if s.sup.cfg.Memories == nil || st.Speaker == "" {
		return st, nil
	}
	got, err := s.sup.cfg.Memories.Recall(ctx, st.Speaker, s.convID, st.HeardAt)
	if err != nil {
		return st, err
	}
	if st.RecalledFor == st.Speaker && slices.Equal(got.Memories, st.Recalled) &&
		slices.EqualFunc(got.Summaries, st.RecalledSummaries, sameSummary) {
		return st, nil
	}
	if err := s.record(journal.Record{
		Kind: journal.KindMemoryRecalled,
		Fields: map[string]string{
			"person":         st.Speaker,
			"memories_json":  journal.EncodeMemories(got.Memories),
			"summaries_json": journal.EncodeSummaries(got.Summaries),
		},
	}); err != nil {
		return st, err
	}
	return s.State(ctx)
}

// sameSummary compares instants, not locations: the log hands back in UTC
// what the store may have handed over in another zone.
func sameSummary(a, b journal.Summary) bool {
	return a.ConversationID == b.ConversationID && a.Text == b.Text && a.At.Equal(b.At)
}

// summarize writes what the conversation that just ended was about and keeps
// it for each identified person who was in it. It runs after the session has
// closed, on the supervisor's background, bounded by SummaryTimeout: the
// people have walked away, and nobody is waiting on it.
//
// Whether it worked is recorded either way, so a missing summary is in the
// log rather than silently absent. Nothing is recorded for a conversation
// with nobody identified in it, or nothing said: there is nobody to keep it
// for, or nothing to keep.
func (s *Session) summarize() {
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

	st, err := s.State(ctx)
	if err != nil {
		s.sup.cfg.Log.Warn("summarize: replay", "conversation", s.convID, "err", err)
		return
	}
	if len(st.Participants) == 0 || !slices.ContainsFunc(st.Dialogue, heard) {
		return
	}
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
