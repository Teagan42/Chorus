package session

import (
	"context"
	"slices"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/registry"
)

// Memories recalls what the model is told it remembers about a person: their
// own memories and what the rest of the household shared (SPEC §5).
type Memories interface {
	Recall(ctx context.Context, person string) ([]journal.Memory, error)
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
// asks with exactly what this turn was given.
func (s *Session) recall(ctx context.Context, st journal.State) (journal.State, error) {
	if s.sup.cfg.Memories == nil || st.Speaker == "" {
		return st, nil
	}
	got, err := s.sup.cfg.Memories.Recall(ctx, st.Speaker)
	if err != nil {
		return st, err
	}
	if st.RecalledFor == st.Speaker && slices.Equal(got, st.Recalled) {
		return st, nil
	}
	if err := s.record(journal.Record{
		Kind: journal.KindMemoryRecalled,
		Fields: map[string]string{
			"person": st.Speaker, "memories_json": journal.EncodeMemories(got),
		},
	}); err != nil {
		return st, err
	}
	return s.State(ctx)
}
