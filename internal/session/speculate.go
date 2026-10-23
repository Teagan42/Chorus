package session

import (
	"context"
	"reflect"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Why a speculation was thrown away, as speculation_discarded records it.
const (
	// DiscardResumed: the person went on speaking through the pause.
	DiscardResumed = "resumed"
	// DiscardChanged: the confirmed turn would have asked something else.
	DiscardChanged = "changed"
	// DiscardNotATurn: the utterance was never answered as a turn.
	DiscardNotATurn = "not_a_turn"
	// DiscardSessionClosed: the session ended before the turn was confirmed.
	DiscardSessionClosed = "session_closed"
)

// Speculation is a turn asked in the pause before its utterance ended. The
// model streams into a buffer: nothing is dispatched and nothing is played.
// The turn that confirms it keeps it only when the log would ask the same
// (ADR-0095); otherwise it is one speculation_discarded.
type Speculation struct {
	s       *Session
	words   Transcript
	speaker string

	// base is the ask before the recollection, and in the ask itself: the
	// confirmed turn must reach both. recalled is the memory_recalled the
	// ask was told, nil when the log already said it.
	base     Input
	in       Input
	recalled *journal.Record

	started time.Time
	cancel  context.CancelFunc

	// more holds one wake-up: an action buffered or the stream closed.
	more    chan struct{}
	drained chan struct{}

	mu      sync.Mutex
	buf     []Action
	end     *TurnEnd
	closed  bool
	settled bool
}

// Speculate asks the model about t before t is confirmed, and returns the
// ask for the confirmed Transcript to carry. Nil when the session will not:
// it is busy, closing, already guessing, t is a hot phrase it answers
// itself, or the ask could not start.
func (s *Session) Speculate(ctx context.Context, t Transcript) *Speculation {
	if t.Text == "" || Hot(t.Text) != "" {
		return nil
	}
	s.mu.Lock()
	idle := s.turnCancel == nil && s.spec == nil && !s.over
	if idle {
		// Held while the ask is prepared, so a second guess finds one.
		s.spec = &Speculation{}
	}
	s.mu.Unlock()
	if !idle {
		return nil
	}
	sp, err := s.speculate(ctx, t)
	if err != nil {
		s.mu.Lock()
		s.spec = nil
		s.mu.Unlock()
		s.sup.cfg.Log.Debug("speculative ask not made", "conversation", s.convID, "err", err)
		return nil
	}
	s.mu.Lock()
	over := s.over
	s.spec = sp
	s.mu.Unlock()
	if over {
		// The close found only the placeholder, so the ask is this one's to end.
		sp.Discard(DiscardSessionClosed)
		return nil
	}
	return sp
}

// speculate folds t into the log as if it had been heard, recalls what that
// turn would be told, and asks.
func (s *Session) speculate(ctx context.Context, t Transcript) (*Speculation, error) {
	st, err := s.State(ctx)
	if err != nil {
		return nil, err
	}
	rec, err := heardRecord(t, "")
	if err != nil {
		return nil, err
	}
	now := s.sup.cfg.Clock.Now()
	if st, err = journal.Suppose(st, now, rec); err != nil {
		return nil, err
	}
	sp := &Speculation{
		s: s, words: t, speaker: st.Speaker, base: s.input(st, t.Text), started: now,
		more: make(chan struct{}, 1), drained: make(chan struct{}),
	}
	recalled, err := s.recollect(ctx, st, t.Text)
	if err != nil {
		return nil, err
	}
	if recalled != nil {
		if st, err = journal.Suppose(st, now, *recalled); err != nil {
			return nil, err
		}
	}
	sp.recalled, sp.in = recalled, s.input(st, t.Text)

	// The session's lifetime, not the caller's: the ask must outlive the
	// listener's guess and is ended by a discard or the turn that keeps it.
	askCtx, cancel := context.WithCancel(s.ctx)
	actions, err := s.sup.cfg.Engine.Turn(askCtx, sp.in)
	if err != nil {
		cancel()
		return nil, err
	}
	sp.cancel = cancel
	go sp.collect(actions)
	return sp, nil
}

// collect buffers the model's stream as it arrives, and keeps draining it
// after a cancel, as Engine requires.
func (sp *Speculation) collect(actions <-chan Action) {
	defer close(sp.drained)
	for a := range actions {
		sp.mu.Lock()
		sp.buf = append(sp.buf, a)
		if end, ok := a.(TurnEnd); ok {
			sp.end = &end
		}
		sp.mu.Unlock()
		sp.wake()
	}
	sp.mu.Lock()
	sp.closed = true
	sp.mu.Unlock()
	sp.wake()
}

func (sp *Speculation) wake() {
	select {
	case sp.more <- struct{}{}:
	default:
	}
}

// settle marks the speculation kept or discarded, and reports whether it was
// still open: whichever comes first wins, once.
func (sp *Speculation) settle() bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.settled {
		return false
	}
	sp.settled = true
	return true
}

// Discard throws the ask away for reason, waits for the model to stop, and
// records what it cost. A nil, kept or already discarded one does nothing.
func (sp *Speculation) Discard(reason string) {
	if sp == nil || !sp.settle() {
		return
	}
	s := sp.s
	s.mu.Lock()
	if s.spec == sp {
		s.spec = nil
	}
	s.mu.Unlock()
	sp.cancel()
	<-sp.drained

	fields := map[string]string{
		"text": sp.words.Text, "reason": reason, "speaker_id": sp.speaker,
		"ran_ms": strconv.FormatInt(s.sup.cfg.Clock.Now().Sub(sp.started).Milliseconds(), 10),
	}
	if sp.end != nil {
		fields["finish_reason"] = sp.end.FinishReason
		if sp.end.Completion != "" {
			fields["completion_json"] = sp.end.Completion
		}
	}
	if err := s.record(journal.Record{Kind: journal.KindSpeculationDiscarded, Speculative: true, Fields: fields}); err != nil {
		s.sup.cfg.Log.Warn("record a discarded speculation", "conversation", s.convID, "err", err)
	}
}

// keep hands the ask to the turn whose transcript carries it, if the log
// would ask the same now that the utterance is in it. It records the
// recollection the ask was told, which is what the turn is told from then
// on, and returns the state with it; a nil speculation means ask afresh.
func (s *Session) keep(ctx context.Context, st journal.State, sp *Speculation, t Transcript) (journal.State, *Speculation, error) {
	if sp.s != s || !sameWords(sp.words, t) || !sameAsk(sp.base, s.input(st, t.Text)) {
		sp.Discard(DiscardChanged)
		return st, nil, nil
	}
	if sp.recalled != nil {
		if err := s.record(*sp.recalled); err != nil {
			sp.Discard(DiscardChanged)
			return st, nil, err
		}
		var err error
		if st, err = s.State(ctx); err != nil {
			sp.Discard(DiscardChanged)
			return st, nil, err
		}
	}
	if !sameAsk(sp.in, s.input(st, t.Text)) {
		sp.Discard(DiscardChanged)
		return st, nil, nil
	}
	if !sp.settle() {
		// The session closed under it.
		return st, nil, nil
	}
	s.mu.Lock()
	if s.spec == sp {
		s.spec = nil
	}
	s.mu.Unlock()
	return st, sp, nil
}

// stream is the kept ask's actions: everything buffered, then the rest as
// the model emits it. Cancelling modelCtx cancels the model, as it would
// an ask made now.
func (sp *Speculation) stream(modelCtx context.Context) <-chan Action {
	context.AfterFunc(modelCtx, sp.cancel)
	out := make(chan Action)
	go func() {
		defer close(out)
		sent := 0
		for {
			sp.mu.Lock()
			ready, closed := sp.buf[sent:], sp.closed
			sp.mu.Unlock()
			for _, a := range ready {
				out <- a
			}
			sent += len(ready)
			if len(ready) > 0 {
				continue
			}
			if closed {
				return
			}
			<-sp.more
		}
	}()
	return out
}

// early is how long before now the kept ask began, as model_completed
// records it.
func (sp *Speculation) early(now time.Time) string {
	return strconv.FormatInt(now.Sub(sp.started).Milliseconds(), 10)
}

// sameWords reports whether the confirmed transcript is the one guessed:
// the same words, attributed the same way.
func sameWords(a, b Transcript) bool {
	return a.Text == b.Text && a.SpeakerID == b.SpeakerID && a.SpeakerMatch == b.SpeakerMatch
}

// sameAsk reports whether two inputs tell the model the same thing, so a
// completion of one is a completion of the other. The time is compared to
// the minute, as the model is told it (internal/provider/chat).
func sameAsk(a, b Input) bool {
	return a.ConversationID == b.ConversationID && a.Speaker == b.Speaker && a.Room == b.Room &&
		a.Text == b.Text && reflect.DeepEqual(a.Dialogue, b.Dialogue) &&
		slices.Equal(a.Memories, b.Memories) && slices.EqualFunc(a.Summaries, b.Summaries, sameSummary) &&
		a.Now.Truncate(time.Minute).Equal(b.Now.Truncate(time.Minute))
}
