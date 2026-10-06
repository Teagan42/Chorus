package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/registry"
)

// Timers is the clock's timer half. The silence backstop and tool timeouts are
// both time questions, so they run on an injected clock (CONTRIBUTING §1).
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// DefaultSilence is the backstop for a model that never calls end_session.
const DefaultSilence = 20 * time.Second

// Reserved tool names the supervisor implements itself. They are ordinary
// registry entries; only their executor is internal.
const (
	toolSpeak      = "speak"
	toolEndSession = "end_session"
)

// Config wires a supervisor. Everything that reads a clock or does I/O is an
// injected interface, so `task test` stays hermetic.
type Config struct {
	Journal       *journal.Journal
	Store         journal.Store
	Clock         journal.Clock
	Timers        Timers
	Engine        Engine
	Speaker       Speaker
	Conversations *Conversations
	Tools         map[string]Tool

	// Specs defaults to registry.Specs, which owns interrupt policy, timeouts,
	// and scoping (ADR-0011).
	Specs map[string]registry.ToolSpec

	Gate    Gate
	Silence time.Duration
}

// Supervisor opens sessions. It holds no per-session state.
type Supervisor struct{ cfg Config }

// New validates the wiring and applies defaults.
func New(cfg Config) (*Supervisor, error) {
	for name, dep := range map[string]any{
		"journal": cfg.Journal, "store": cfg.Store, "clock": cfg.Clock,
		"timers": cfg.Timers, "engine": cfg.Engine, "speaker": cfg.Speaker,
	} {
		if dep == nil {
			return nil, fmt.Errorf("session: %s is required", name)
		}
	}
	if cfg.Specs == nil {
		cfg.Specs = registry.Specs
	}
	if cfg.Tools == nil {
		cfg.Tools = map[string]Tool{}
	}
	if cfg.Conversations == nil {
		cfg.Conversations = NewConversations(cfg.Clock, MigrationWindow)
	}
	if cfg.Silence == 0 {
		cfg.Silence = DefaultSilence
	}
	return &Supervisor{cfg: cfg}, nil
}

// Wake is a confirmed wake word. The satellite owns the audio stream; the
// person owns the conversation (SPEC §4.5).
type Wake struct {
	Satellite  string
	PersonID   string
	Confidence float64
}

// Transcript is a final STT result.
type Transcript struct {
	Text      string
	SpeakerID string
	AudioRef  string
}

// Session is the supervisor of one conversation's concurrent children. It
// keeps no authoritative state: everything is derived from the log (SPEC §8).
type Session struct {
	sup       *Supervisor
	convID    string
	satellite string
	person    string
	resumed   bool

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	// activity defers the silence backstop without restarting its goroutine.
	activity chan struct{}

	// appendMu serialises writes: journal.Append reads the last sequence
	// number then writes, and concurrent children would collide.
	appendMu sync.Mutex

	mu         sync.Mutex
	children   map[string]int
	turnCancel context.CancelFunc
	turnErr    error
	implicitID string

	speech    *speechChannel
	closeOnce sync.Once
}

// Open starts a session for a confirmed wake word, resuming the person's
// conversation when they have moved to another satellite (SPEC §4.5).
func (sup *Supervisor) Open(ctx context.Context, w Wake) (*Session, error) {
	convID, resumed := sup.cfg.Conversations.Open(w.PersonID)

	s := &Session{
		sup: sup, convID: convID, satellite: w.Satellite, person: w.PersonID,
		resumed:  resumed,
		done:     make(chan struct{}),
		activity: make(chan struct{}, 1),
		children: map[string]int{},
	}
	// WithoutCancel: a session outlives the request that woke it, and only the
	// supervisor ends its children (CONTRIBUTING §6).
	s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	s.speech = newSpeechChannel(s)

	fields := map[string]string{"satellite": w.Satellite, "speaker_id": w.PersonID}
	if w.Confidence > 0 {
		fields["wake_confidence"] = strconv.FormatFloat(w.Confidence, 'f', -1, 64)
	}
	if err := s.record(journal.Record{Kind: journal.KindSessionOpened, Fields: fields}); err != nil {
		s.cancel()
		return nil, err
	}
	s.enter("listening")
	go s.backstop()
	return s, nil
}

// ConversationID is the person's conversation, not the device's stream.
func (s *Session) ConversationID() string { return s.convID }

// Resumed reports that this wake word rejoined an existing conversation.
func (s *Session) Resumed() bool { return s.resumed }

// Done closes when the session ends.
func (s *Session) Done() <-chan struct{} { return s.done }

// Children names the live children. That set *is* the lifecycle: there is no
// stage enum (SPEC §4).
func (s *Session) Children() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.children))
	for name, n := range s.children {
		if n > 0 {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// State derives the session from its log.
func (s *Session) State(ctx context.Context) (journal.State, error) {
	return journal.Replay(ctx, s.sup.cfg.Store, s.convID, journal.Overrides{})
}

// Heard journals an utterance and runs a turn over it.
func (s *Session) Heard(ctx context.Context, t Transcript) error {
	if err := s.record(journal.Record{
		Kind:     journal.KindUtteranceTranscribed,
		AudioRef: t.AudioRef,
		Fields:   map[string]string{"text": t.Text, "speaker_id": t.SpeakerID},
	}); err != nil {
		return err
	}
	if t.SpeakerID != "" {
		// A confident mismatch flips attribution inside the conversation (§5).
		s.mu.Lock()
		s.person = t.SpeakerID
		s.mu.Unlock()
	}
	s.sup.cfg.Conversations.Touch(s.person)
	s.poke()
	return s.turn(ctx, t)
}

// turn runs the Thinking child: it drains the action stream, dispatching each
// action as it arrives, and never reorders it (SPEC §4.1).
func (s *Session) turn(ctx context.Context, t Transcript) error {
	st, err := s.State(ctx)
	if err != nil {
		return err
	}

	s.speech.resume()
	turnCtx, cancel := context.WithCancel(s.ctx)
	defer cancel()

	s.mu.Lock()
	s.turnCancel, s.turnErr, s.implicitID = cancel, nil, newID()
	s.mu.Unlock()

	actions, err := s.sup.cfg.Engine.Turn(turnCtx, Input{
		ConversationID: s.convID,
		Speaker:        st.Speaker,
		Text:           t.Text,
	})
	if err != nil {
		return fmt.Errorf("turn %s: %w", s.convID, err)
	}

	s.enter("thinking")
	defer s.leave("thinking")

	var wg sync.WaitGroup
	open := map[string]bool{}
	dropped := map[string]string{}
	for a := range actions {
		switch act := a.(type) {
		case SpeechDelta:
			s.deliverSpeech(turnCtx, act, open, dropped)
		case ToolCall:
			s.dispatch(turnCtx, &wg, act)
		case TurnEnd:
			s.fail(s.record(journal.Record{
				Kind: journal.KindModelCompleted,
				Fields: map[string]string{
					"completion_json": act.Completion,
					"finish_reason":   act.FinishReason,
				},
			}))
		}
	}
	// Whatever the model was still generating when it was cut off.
	for _, id := range slices.Sorted(maps.Keys(dropped)) {
		s.discardSpeech(id, dropped[id])
	}
	wg.Wait()
	s.speech.waitIdle()
	s.poke()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCancel = nil
	return s.turnErr
}

// deliverSpeech routes a delta to the speech channel. Inline content carries
// no call id, and is recorded as an implicit speak so the journal has one
// representation of speech (ADR-0003).
func (s *Session) deliverSpeech(turnCtx context.Context, d SpeechDelta, open map[string]bool, dropped map[string]string) {
	if d.CallID == "" {
		s.mu.Lock()
		d.CallID = s.implicitID
		s.mu.Unlock()
	}
	if !open[d.CallID] {
		open[d.CallID] = true
		s.fail(s.record(journal.Record{
			Kind: journal.KindToolCalled,
			Fields: map[string]string{
				"tool": toolSpeak, "call_id": d.CallID,
				"args_json": fmt.Sprintf(`{"mode":%q,"streamed":true}`, mode(d.Mode)),
			},
		}))
	}
	// A delta that arrives after the cut was generated but never heard. It is
	// recorded, not played, and not silently thrown away (SPEC §4.2).
	if turnCtx.Err() == nil && s.speech.deliver(d) {
		return
	}
	dropped[d.CallID] += d.Text
	if d.Last {
		s.discardSpeech(d.CallID, dropped[d.CallID])
		delete(dropped, d.CallID)
	}
}

// discardSpeech records text the model generated for a turn that was cut off.
func (s *Session) discardSpeech(callID, text string) {
	if text != "" {
		s.fail(s.record(journal.Record{
			Kind: journal.KindSpeechDiscarded,
			Fields: map[string]string{
				"unspoken_text": text, "reason": "barge_in",
			},
		}))
	}
	s.result(callID, "cancelled", "")
}

// dispatch journals the call in stream order, then forks a child. Journalling
// before the fork is what keeps the log in the order the model chose.
func (s *Session) dispatch(ctx context.Context, wg *sync.WaitGroup, tc ToolCall) {
	if err := s.record(journal.Record{
		Kind: journal.KindToolCalled,
		Fields: map[string]string{
			"tool": tc.Tool, "call_id": tc.ID, "args_json": tc.Args,
		},
	}); err != nil {
		s.fail(err)
		return
	}

	spec, declared := s.sup.cfg.Specs[tc.Tool]
	if !declared {
		s.result(tc.ID, "error", `{"error":"unknown_tool"}`)
		return
	}

	switch tc.Tool {
	case toolSpeak:
		var args struct {
			Text string `json:"text"`
			Mode Mode   `json:"mode"`
		}
		if err := json.Unmarshal([]byte(tc.Args), &args); err != nil {
			s.result(tc.ID, "error", `{"error":"bad_arguments"}`)
			return
		}
		// The speech child journals this call's result when playback ends.
		if !s.speech.deliver(SpeechDelta{CallID: tc.ID, Text: args.Text, Mode: args.Mode, Last: true}) {
			s.discardSpeech(tc.ID, args.Text)
		}
		return
	case toolEndSession:
		s.result(tc.ID, "ok", "")
		// Close cancels this turn, so it cannot run on the turn goroutine.
		go func() { s.fail(s.Close(context.Background(), "model_ended")) }()
		return
	}

	tool, ok := s.sup.cfg.Tools[tc.Tool]
	if !ok {
		s.result(tc.ID, "error", `{"error":"not_implemented"}`)
		return
	}
	wg.Add(1)
	s.enter("tool:" + tc.ID)
	go s.runTool(ctx, wg, tc, spec, tool)
}

// runTool is one tool child. Its context comes from the declared interrupt
// policy, so barge-in needs no special case here (SPEC §4.4).
func (s *Session) runTool(parent context.Context, wg *sync.WaitGroup, tc ToolCall, spec registry.ToolSpec, tool Tool) {
	defer s.leave("tool:" + tc.ID)

	var once sync.Once
	release := func() { once.Do(wg.Done) }
	defer release()

	ctx, cancel := s.policyContext(parent, spec.OnInterrupt)
	defer cancel()

	type outcome struct {
		out string
		err error
	}
	results := make(chan outcome, 1)
	go func() {
		out, err := tool.Invoke(ctx, tc.Args)
		results <- outcome{out, err}
	}()

	timeout := s.sup.cfg.Timers.After(spec.Timeout)
	interrupted := parent.Done()
	detached := false

	for {
		select {
		case r := <-results:
			switch {
			case errors.Is(r.err, context.Canceled):
				s.result(tc.ID, "cancelled", "")
			case r.err != nil:
				// Recoverable failures are results the model reasons about
				// rather than canned speech (SPEC §7).
				s.result(tc.ID, "error", fmt.Sprintf(`{"error":%q}`, r.err.Error()))
			case detached:
				// Kept, but nobody was waiting for it any more.
				s.result(tc.ID, "detached", r.out)
			default:
				s.result(tc.ID, "ok", r.out)
			}
			return
		case <-timeout:
			s.result(tc.ID, "timed_out", `{"error":"timed_out"}`)
			return
		case <-interrupted:
			// nil the channel: a closed Done would spin this loop.
			interrupted = nil
			if spec.OnInterrupt == registry.InterruptDetach {
				detached = true
				release()
			}
		}
	}
}

// policyContext maps the registry's on_interrupt onto cancellation.
func (s *Session) policyContext(parent context.Context, p registry.InterruptPolicy) (context.Context, context.CancelFunc) {
	switch p {
	case registry.InterruptDetach:
		// Outlives the barge-in, dies with the session: wasted but harmless.
		return context.WithCancel(s.ctx)
	case registry.InterruptUninterruptible:
		// Side effects are already committed, so nothing may stop it — not a
		// barge-in and not the session ending.
		return context.WithCancel(context.WithoutCancel(s.ctx))
	default:
		return context.WithCancel(parent)
	}
}

// BargeIn applies the detection gate and, when it passes, stops speech and
// cancels this turn's interruptible children. Rejections are journalled as
// the tuning corpus for the gate (SPEC §4.3).
func (s *Session) BargeIn(_ context.Context, c Candidate) (bool, error) {
	s.mu.Lock()
	speaker := s.person
	cancel := s.turnCancel
	s.mu.Unlock()

	if stage, ok := s.sup.cfg.Gate.admit(c, speaker); !ok {
		return false, s.record(journal.Record{
			Kind: journal.KindBargeInRejected, AudioRef: c.AudioRef,
			Fields: map[string]string{"stage": stage},
		})
	}
	if err := s.record(journal.Record{
		Kind: journal.KindBargeInDetected, AudioRef: c.AudioRef,
		Fields: map[string]string{"tts_position_ms": strconv.Itoa(c.PositionMS)},
	}); err != nil {
		return false, err
	}
	s.poke()
	s.speech.interrupt("barge_in")
	if cancel != nil {
		cancel()
	}
	return true, nil
}

// Close ends the session. Reason must be a declared session_closed reason.
func (s *Session) Close(_ context.Context, reason string) error {
	var err error
	s.closeOnce.Do(func() {
		// Discards are recorded before the close that caused them.
		s.speech.interrupt("session_closed")
		err = s.record(journal.Record{
			Kind:   journal.KindSessionClosed,
			Fields: map[string]string{"reason": reason},
		})
		s.leave("listening")
		s.cancel()
		close(s.done)
	})
	return err
}

// backstop closes a session the model forgot to end. The model knows a task
// is finished better than a timer does, so this is only a floor (SPEC §4.5).
func (s *Session) backstop() {
	for {
		silence := s.sup.cfg.Timers.After(s.sup.cfg.Silence)
		select {
		case <-s.done:
			return
		case <-s.activity:
		case <-silence:
			if s.busy() {
				continue
			}
			s.fail(s.Close(context.Background(), "silence_timeout"))
			return
		}
	}
}

// busy reports whether any child beyond Listening is alive.
func (s *Session) busy() bool {
	for _, name := range s.Children() {
		if name != "listening" {
			return true
		}
	}
	return false
}

func (s *Session) poke() {
	select {
	case s.activity <- struct{}{}:
	default:
	}
}

func (s *Session) result(callID, outcome, result string) {
	fields := map[string]string{"call_id": callID, "outcome": outcome}
	if result != "" {
		fields["result_json"] = result
	}
	s.fail(s.record(journal.Record{Kind: journal.KindToolResult, Fields: fields}))
}

// record stamps an event through the journal. WithoutCancel: a child that was
// just cancelled still has to record what it did (SPEC §4.4).
func (s *Session) record(r journal.Record) error {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	_, err := s.sup.cfg.Journal.Append(context.WithoutCancel(s.ctx), s.convID, r)
	return err
}

// fail keeps the first error of the current turn for Heard to return.
func (s *Session) fail(err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnErr == nil {
		s.turnErr = err
	}
}

func (s *Session) enter(child string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.children[child]++
}

func (s *Session) leave(child string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.children[child]--
}

func mode(m Mode) Mode {
	if m == "" {
		return ModeQueue
	}
	return m
}
