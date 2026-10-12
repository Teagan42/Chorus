package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// DefaultModelTimeout is how long an ask may go without the model emitting
// anything before it is given up on. Longer than a cold load (~71 s
// measured, internal/provider/ollama): Ollama aborts a load whose request is
// cancelled, so a shorter deadline would leave a model evicted overnight
// unloadable, every ask cancelling the load the next one needs (ADR-0051).
const DefaultModelTimeout = 90 * time.Second

// Canned is what is said when there is no model left to reason with, no
// voice left to say what it reasoned, or no ear to hear what was asked
// (SPEC §7). Everything recoverable is a tool result the model words
// itself; these are not.
type Canned struct {
	// Model is said when the model cannot answer a turn.
	Model string

	// Voice is said when the synthesiser fails mid-turn. It can only be
	// heard if it was rendered while the synthesiser still answered, which
	// is what satellite.Canned is for.
	Voice string

	// Ear is said when speech-to-text fails on what someone said (ADR-0069).
	Ear string
}

// DefaultCanned is the household's apology for each failure: short, true,
// and saying what to do next.
var DefaultCanned = Canned{
	Model: "Sorry, I can't think straight right now. Give me a minute and ask again.",
	Voice: "Sorry, I've lost my voice for a moment.",
	Ear:   "Sorry, my hearing isn't working right now. Try again in a minute.",
}

// Lines are the canned lines, for rendering ahead of time.
func (c Canned) Lines() []string { return []string{c.Model, c.Voice, c.Ear} }

// Model failure reasons, as model_failed records them.
const (
	modelUnavailable = "unavailable"
	modelFailedMid   = "failed"
	modelTimedOut    = "timed_out"
)

// modelFailure is an ask the model let down, as opposed to one a barge-in
// or the session's end cut short. The turn ends on it, and says so.
type modelFailure struct {
	reason string
	err    string
}

func (f *modelFailure) Error() string { return "model " + f.reason + ": " + f.err }

// watchdog gives up on an ask that has emitted nothing for timeout. The model
// client has no timeout of its own, deliberately: a turn streams for as long
// as the model talks (internal/provider/ollama). So the deadline is measured
// from the last thing the model did, and every action restarts it.
type watchdog struct {
	alive  chan struct{}
	done   chan struct{}
	exited chan struct{}

	mu    sync.Mutex
	fired bool
}

// watch starts the deadline over the ask whose context cancel ends.
func (s *Session) watch(cancel context.CancelFunc) *watchdog {
	w := &watchdog{
		alive:  make(chan struct{}, 1),
		done:   make(chan struct{}),
		exited: make(chan struct{}),
	}
	go func() {
		defer close(w.exited)
		for {
			select {
			case <-w.done:
				return
			case <-w.alive:
			case <-s.sup.cfg.Timers.After(s.sup.cfg.ModelTimeout):
				// The model may have acted as the deadline came due, and
				// select picks between the two at random.
				select {
				case <-w.alive:
					continue
				default:
				}
				w.mu.Lock()
				w.fired = true
				w.mu.Unlock()
				cancel()
				return
			}
		}
	}()
	return w
}

// heard restarts the deadline: the model did something.
func (w *watchdog) heard() {
	select {
	case w.alive <- struct{}{}:
	default:
	}
}

// stop ends the watch and reports whether the deadline gave up on the ask.
func (w *watchdog) stop() bool {
	close(w.done)
	<-w.exited
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.fired
}

// modelFailed journals a model that could not answer and says the canned
// line in its place, once per turn: the house does not go silent, and the
// failure is training data like any other (SPEC §7, ADR-0051).
func (s *Session) modelFailed(f *modelFailure) {
	s.sup.cfg.Log.Warn("model failed", "conversation", s.convID, "reason", f.reason, "err", f.err)
	s.sup.fault(FaultModel)
	id := s.speech.claimCanned()
	fields := map[string]string{"reason": f.reason, "error": f.err}
	if id != "" {
		fields["canned_call_id"] = id
	}
	if err := s.record(journal.Record{Kind: journal.KindModelFailed, Fields: fields}); err != nil {
		s.fail(err)
		return
	}
	if id != "" {
		s.sayCanned(id, s.sup.cfg.Canned.Model, false)
	}
}

// Unheard answers an utterance the ear failed on, in this session, with the
// ear's canned line, once it is this utterance's turn (SPEC §7). No event
// can hold words nothing decoded, so the line, marked unheard, is the record
// that something was said (ADR-0069).
func (s *Session) Unheard(_ context.Context, ended time.Time) error {
	s.speech.resume(ended)
	return s.sayUnheard(s.speech.claimCanned())
}

// sayUnheard says the ear's line as the claimed call id, as a turn of its
// own, and returns once it has played.
func (s *Session) sayUnheard(id string) error {
	s.mu.Lock()
	// A turn with no model to cancel, so a lost write is returned.
	s.turnCancel, s.turnErr = func() {}, nil
	s.mu.Unlock()
	if id != "" {
		s.sayCanned(id, s.sup.cfg.Canned.Ear, true)
	}
	s.speech.waitIdle()
	s.poke()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnCancel = nil
	return s.turnErr
}

// Misheard records rejected in log, naming the ear's canned line, then says
// that line on satellite with no session open: a wake whose words the ear
// failed on, from a voice that would have confirmed it (SPEC §7, §9.3). It
// returns once the line has played, failed, or been cut by ctx ending, as
// the device's link does. A rejection the journal refuses is not
// apologised for: the log is the runtime (ADR-0007).
func (sup *Supervisor) Misheard(ctx context.Context, satellite, log string, rejected journal.Record) error {
	// No session: the speech is the device's, like the rejection it answers.
	s := &Session{
		sup: sup, convID: log, satellite: satellite,
		done: make(chan struct{}), closing: make(chan struct{}), activity: make(chan struct{}, 1), children: map[string]int{},
	}
	s.ctx, s.cancel = context.WithCancel(context.WithoutCancel(ctx))
	defer s.cancel()
	s.speech = newSpeechChannel(s)

	id := s.speech.claimCanned()
	rejected.Fields = maps.Clone(rejected.Fields)
	if rejected.Fields == nil {
		rejected.Fields = map[string]string{}
	}
	rejected.Fields["canned_call_id"] = id
	if err := s.record(rejected); err != nil {
		return err
	}
	// Cut as a session's speech is cut when its device goes.
	defer context.AfterFunc(ctx, func() { s.speech.shut("session_closed") })()
	return s.sayUnheard(id)
}

// sayCanned journals a canned line as a speak call of its own, marked so
// the dialogue and the harvest know nobody's turn chose it, and queues it
// past any cut. The caller has claimed it (speechChannel.claimCanned).
func (s *Session) sayCanned(id, text string, unheard bool) {
	if !s.cannedCall(id, text, unheard) {
		return
	}
	if !s.speech.canned(id, text) {
		s.discardSpeech(id, text)
	}
}

// sayCannedLocked is sayCanned from inside the speech channel's lock.
func (s *Session) sayCannedLocked(id, text string) {
	if !s.cannedCall(id, text, false) {
		return
	}
	if !s.speech.cannedLocked(id, text) {
		s.speech.discardedLocked(id, text, s.speech.cutReasonLocked())
		s.result(id, "cancelled", "")
	}
}

// cannedCall journals the speak call a canned line is said as. unheard
// marks the ear's, which answers words no event holds.
func (s *Session) cannedCall(id, text string, unheard bool) bool {
	// Strings and bools: marshalling cannot fail.
	args, _ := json.Marshal(struct {
		Text    string `json:"text"`
		Mode    Mode   `json:"mode"`
		Canned  bool   `json:"canned"`
		Unheard bool   `json:"unheard,omitempty"`
	}{Text: text, Mode: ModeQueue, Canned: true, Unheard: unheard})
	err := s.record(journal.Record{
		Kind:   journal.KindToolCalled,
		Fields: map[string]string{"tool": toolSpeak, "call_id": id, "args_json": string(args)},
	})
	s.fail(err)
	return err == nil
}

// failure classifies an ask that ended without an answer: msg is what the
// engine said, empty when it said nothing. A cut turn is not a failed one:
// the barge-in or the session's end is the reason, and is already recorded.
func (s *Session) failure(cut, stalled, started bool, msg string) *modelFailure {
	switch {
	case cut:
		return nil
	case stalled:
		return &modelFailure{reason: modelTimedOut, err: fmt.Sprintf("nothing from the model for %v", s.sup.cfg.ModelTimeout)}
	case !started:
		return &modelFailure{reason: modelUnavailable, err: msg}
	case msg == "":
		return &modelFailure{reason: modelFailedMid, err: "the stream ended in error"}
	default:
		return &modelFailure{reason: modelFailedMid, err: msg}
	}
}

// isModelFailure unwraps a failure the turn says rather than returns.
func isModelFailure(err error) (*modelFailure, bool) {
	var f *modelFailure
	return f, errors.As(err, &f)
}
