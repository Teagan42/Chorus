package session_test

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// clock is a virtual clock with deadline-ordered timers. Barge-in and the
// silence backstop are millisecond questions; time.Sleep makes them flaky
// (CONTRIBUTING §1).
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*vtimer
}

type vtimer struct {
	deadline time.Time
	waited   time.Duration
	ch       chan time.Time
}

func newClock() *clock { return &clock{now: epoch} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &vtimer{deadline: c.now.Add(d), waited: d, ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// advance moves the clock and expires every timer that came due. Unrelated
// timers are left armed, so firing the backstop does not trip a tool timeout.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due, live []*vtimer
	for _, t := range c.timers {
		if t.deadline.After(c.now) {
			live = append(live, t)
			continue
		}
		due = append(due, t)
	}
	c.timers = live
	c.mu.Unlock()

	for _, t := range due {
		t.ch <- t.deadline
	}
}

// awaitTimers spins until n timers are armed, so a test never fires a timer
// the implementation has not requested yet.
func (c *clock) awaitTimers(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		armed := len(c.timers)
		c.mu.Unlock()
		if armed >= n {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("fewer than %d timers armed", n)
}

// waits reports the delays currently armed.
func (c *clock) waits() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, 0, len(c.timers))
	for _, t := range c.timers {
		out = append(out, t.waited)
	}
	return out
}
