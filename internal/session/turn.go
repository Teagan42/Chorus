package session

import (
	"context"

	"github.com/teaganglenn/chorus/internal/journal"
)

// Mode selects how a speak call joins the speech channel (SPEC §4.2).
type Mode string

const (
	// ModeQueue appends after current speech. Natural for
	// "one sec" -> tool result -> "found three".
	ModeQueue Mode = "queue"
	// ModePreempt drops current and pending speech, for when a tool result
	// invalidates what was about to be said.
	ModePreempt Mode = "preempt"
	// ModeInterject cuts in front of the queue but keeps it.
	ModeInterject Mode = "interject"
)

// Action is one item in the model's stream of concurrent actions. The model
// opens and closes actions at will and the orchestrator imposes no order
// (SPEC §4.1).
type Action interface{ action() }

// SpeechDelta is speech as emitted. CallID groups deltas into one utterance;
// deltas stream to TTS rather than waiting for end of message.
type SpeechDelta struct {
	CallID string
	Text   string
	Mode   Mode

	// Last marks the final delta of this utterance.
	Last bool
}

// ToolCall is dispatched the instant its JSON closes.
type ToolCall struct {
	ID   string
	Tool string
	Args string
}

// TurnEnd reports the raw completion, which replay cannot regenerate (SPEC §8).
type TurnEnd struct {
	FinishReason string
	Completion   string
}

func (SpeechDelta) action() {}
func (ToolCall) action()    {}
func (TurnEnd) action()     {}

// Engine is the turn engine seam (SPEC §12). Phase 1 is the streaming cascade;
// a speech-to-speech provider later maps its events onto these.
type Engine interface {
	// Turn emits actions until it closes the channel. The caller cancels ctx,
	// and must keep draining afterwards: a cancelled turn still closes its open
	// utterance and still reports its TurnEnd, and abandoning the channel both
	// loses those and strands the provider.
	Turn(ctx context.Context, in Input) (<-chan Action, error)
}

// Input is what the model sees: the heard transcript plus derived state.
type Input struct {
	ConversationID string
	Speaker        string

	// Text is the utterance this turn answers.
	Text string

	// Dialogue is the conversation so far, derived from the log: every
	// earlier turn, this turn's utterance, and on a follow-up ask the calls
	// this turn made and what they returned (SPEC §4.4). Empty for an engine
	// asked a turn on its own, which then has only Text.
	Dialogue []journal.Entry

	// Memories are what the model is told it remembers, as the log recorded
	// them for this speaker, newest first. Empty for a guest (SPEC §5).
	Memories []journal.Memory
}

// Tool is one executable registry entry. Failures come back as errors and
// become tool results the model reasons about (SPEC §7).
type Tool interface {
	Invoke(ctx context.Context, args string) (string, error)
}

// ToolFunc adapts a function to Tool.
type ToolFunc func(ctx context.Context, args string) (string, error)

// Invoke calls f.
func (f ToolFunc) Invoke(ctx context.Context, args string) (string, error) {
	return f(ctx, args)
}

// Classifier is a Tool that can say what a call acts on before it runs, so
// a confirm_when entry naming target classes holds the garage door and not
// the blinds (ADR-0041). The orchestrator's arguments are already off.
type Classifier interface {
	Classify(ctx context.Context, args string) ([]string, error)
}
