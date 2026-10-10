// Package timer keeps the household's timers in journal.HouseTimers; the
// scheduler's state is that log reduced. A timer goes off where it was set
// (ADR-0045).
package timer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/announce"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// DefaultGrace is how late a timer may still be said. One due longer ago,
// while the daemon was down, is recorded as missed: the pasta is past saving.
const DefaultGrace = 5 * time.Minute

// RetryEvery is how often a timer tries a satellite that is not connected:
// it may be rebooting, or not yet dialled back in.
const RetryEvery = 5 * time.Second

// MaxDuration bounds one timer. A day covers the slow cooker; anything
// longer is a reminder, which a timer is not.
const MaxDuration = 24 * time.Hour

// Config wires a scheduler. Everything that reads a clock or does I/O is
// injected, so `task test` stays hermetic (CONTRIBUTING §1).
type Config struct {
	Journal   *journal.Journal
	Store     journal.Store
	Clock     journal.Clock
	Timers    session.Timers
	Announcer announce.Announcer

	// Grace defaults to DefaultGrace.
	Grace time.Duration

	// Log is where a timer that could not be recorded is reported. Nil
	// discards.
	Log *slog.Logger
}

// Request is a timer to start, as the model asked for it.
type Request struct {
	Seconds      int
	Label        string
	Announcement string
}

// Scheduler runs the household's timers. Its state is the house log
// reduced: every event it writes is folded through journal.Reduce, exactly
// as a replay of the log would fold it.
type Scheduler struct {
	cfg Config
	ctx context.Context
	wg  sync.WaitGroup

	mu    sync.Mutex
	state journal.State
	// armed holds each running timer's stop. One that is going off has left
	// it, so a cancel cannot race the announcement into a log that ends a
	// timer twice.
	armed map[string]chan struct{}
}

// Start replays the house log and arms every timer still running. Those
// that came due while the daemon was down go off now, or are missed.
func Start(ctx context.Context, cfg Config) (*Scheduler, error) {
	switch {
	case cfg.Journal == nil, cfg.Store == nil, cfg.Clock == nil, cfg.Timers == nil:
		return nil, errors.New("timer: journal, store, clock and timers are required")
	case cfg.Announcer == nil:
		return nil, errors.New("timer: an announcer is required")
	}
	if cfg.Grace == 0 {
		cfg.Grace = DefaultGrace
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	st, err := journal.Replay(ctx, cfg.Store, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		return nil, fmt.Errorf("timer: %w", err)
	}
	s := &Scheduler{cfg: cfg, ctx: ctx, state: st, armed: map[string]chan struct{}{}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range st.Running() {
		s.armLocked(t)
	}
	return s, nil
}

// Wait blocks until every armed timer's goroutine has exited, which they do
// when the scheduler's context ends.
func (s *Scheduler) Wait() { s.wg.Wait() }

// Running are the timers still to go off, soonest first.
func (s *Scheduler) Running() []journal.Timer {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Running()
}

// Set starts a timer for the caller, on the satellite the call was made on.
func (s *Scheduler) Set(ctx context.Context, c session.Caller, r Request) (journal.Timer, error) {
	d := time.Duration(r.Seconds) * time.Second
	switch {
	case r.Seconds <= 0:
		return journal.Timer{}, errors.New("a timer needs a duration of at least a second")
	case d > MaxDuration:
		return journal.Timer{}, fmt.Errorf("a timer can run for at most %v", MaxDuration)
	case c.Satellite == "":
		return journal.Timer{}, errors.New("there is no satellite for the timer to go off on")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fields := map[string]string{
		"timer_id": newID(), "seconds": strconv.Itoa(r.Seconds),
		"fires_at":  s.cfg.Clock.Now().Add(d).UTC().Format(time.RFC3339Nano),
		"satellite": c.Satellite, "label": strings.TrimSpace(r.Label),
		"announcement": strings.TrimSpace(r.Announcement), "person": c.Person,
		"conversation_id": c.ConversationID, "call_id": c.CallID,
	}
	if err := s.recordLocked(ctx, journal.KindTimerStarted, fields); err != nil {
		return journal.Timer{}, err
	}
	t, _ := s.state.Timer(fields["timer_id"])
	s.armLocked(t)
	return t, nil
}

// Cancel stops a running timer before it goes off.
func (s *Scheduler) Cancel(ctx context.Context, c session.Caller, id string) (journal.Timer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.state.Timer(id)
	stop, armed := s.armed[id]
	switch {
	case !ok:
		return journal.Timer{}, fmt.Errorf("no timer has id %q", id)
	case t.Status != journal.TimerRunning:
		return journal.Timer{}, fmt.Errorf("timer %s has already %s", id, t.Status)
	case !armed:
		return journal.Timer{}, fmt.Errorf("timer %s is going off now", id)
	}
	if err := s.recordLocked(ctx, journal.KindTimerCancelled, map[string]string{
		"timer_id": id, "conversation_id": c.ConversationID, "call_id": c.CallID,
	}); err != nil {
		return journal.Timer{}, err
	}
	close(stop)
	delete(s.armed, id)
	t, _ = s.state.Timer(id)
	return t, nil
}

// armLocked waits out a running timer on its own goroutine. The wait is
// armed here, under the lock, so a clock that moves next cannot miss it.
func (s *Scheduler) armLocked(t journal.Timer) {
	stop := make(chan struct{})
	s.armed[t.ID] = stop
	var due <-chan time.Time
	if wait := t.FiresAt.Sub(s.cfg.Clock.Now()); wait > 0 {
		due = s.cfg.Timers.After(wait)
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if due != nil {
			select {
			case <-s.ctx.Done():
				return
			case <-stop:
				return
			case <-due:
			}
		}
		s.fire(t.ID)
	}()
}

// fire says a timer, then records what became of it. A daemon dying between
// the two says it again on restart, which beats never.
func (s *Scheduler) fire(id string) {
	s.mu.Lock()
	t, ok := s.state.Timer(id)
	if _, armed := s.armed[id]; !ok || !armed || t.Status != journal.TimerRunning {
		s.mu.Unlock()
		return
	}
	delete(s.armed, id)
	s.mu.Unlock()

	fields := map[string]string{"timer_id": id}
	switch late := s.cfg.Clock.Now().Sub(t.FiresAt); {
	case late > s.cfg.Grace:
		fields["outcome"] = "missed"
		fields["error"] = fmt.Sprintf("came due %v ago, while chorusd was down", late.Round(time.Second))
	default:
		conv, err := s.announce(t)
		if err != nil {
			fields["outcome"], fields["error"] = "unannounced", fmt.Sprintf("%s: %v", t.Satellite, err)
		} else {
			fields["outcome"], fields["conversation_id"] = "announced", conv
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The daemon's own context may be ending: the record still goes in.
	if err := s.recordLocked(context.WithoutCancel(s.ctx), journal.KindTimerFinished, fields); err != nil {
		s.cfg.Log.Warn("record a timer going off", "timer", id, "err", err)
	}
}

// errNotHeard is an announcement queued on a satellite that never played it.
var errNotHeard = errors.New("not heard")

// announce says a timer on its satellite, trying again while it is not
// connected or not heard and the timer is still within its grace.
func (s *Scheduler) announce(t journal.Timer) (string, error) {
	for {
		heard := make(chan bool, 1)
		conv, err := s.cfg.Announcer.Announce(s.ctx, t.Satellite, session.Announcement{
			Text: Says(t), Source: session.SourceTimer, TimerID: t.ID, RequestedBy: t.Person,
			FromSatellite: t.Satellite, FromConversation: t.ConversationID, Heard: heard,
		})
		if err == nil {
			select {
			case ok := <-heard:
				if !ok {
					err = errNotHeard
				}
			case <-s.ctx.Done():
				return conv, s.ctx.Err()
			}
		}
		late := s.cfg.Clock.Now().Sub(t.FiresAt)
		retry := errors.Is(err, announce.ErrNotConnected) || errors.Is(err, errNotHeard)
		if !retry || late+RetryEvery > s.cfg.Grace {
			return conv, err
		}
		select {
		case <-s.ctx.Done():
			return "", err
		case <-s.cfg.Timers.After(RetryEvery):
		}
	}
}

// recordLocked appends to the house log and folds the event in, as replay
// would. An event the reducer refuses once written is a scheduler bug.
func (s *Scheduler) recordLocked(ctx context.Context, k journal.Kind, fields map[string]string) error {
	for f, v := range fields {
		if v == "" {
			delete(fields, f)
		}
	}
	e, err := s.cfg.Journal.Append(ctx, journal.HouseTimers, journal.Record{Kind: k, Fields: fields})
	if err != nil {
		return fmt.Errorf("record %s: %w", k, err)
	}
	st, err := journal.Reduce(s.state, e)
	if err != nil {
		return fmt.Errorf("fold %s seq %d: %w", k, e.Seq, err)
	}
	s.state = st
	return nil
}

// Says is what a timer says when it goes off: what the model asked for, or
// the label's own words when it asked for nothing.
func Says(t journal.Timer) string {
	switch {
	case t.Announcement != "":
		return t.Announcement
	case t.Label != "":
		return "The " + t.Label + " timer is done."
	default:
		return "Your timer is done."
	}
}

func newID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("timer: crypto/rand failed: " + err.Error())
	}
	return "t_" + hex.EncodeToString(b[:])
}
