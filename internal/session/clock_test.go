package session_test

import (
	"sync"
	"time"
)

var epoch = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// clock is a settable virtual clock. Barge-in and the silence backstop are
// millisecond questions; time.Sleep makes them flaky (CONTRIBUTING §1).
type clock struct {
	mu    sync.Mutex
	now   time.Time
	fired []chan time.Time
	waits []time.Duration
}

func newClock() *clock { return &clock{now: epoch} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// After records the requested delay and hands back a channel the test fires.
func (c *clock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fired = append(c.fired, ch)
	c.waits = append(c.waits, d)
	return ch
}

// fire expires every outstanding timer at the current virtual time.
func (c *clock) fire() {
	c.mu.Lock()
	chans, now := c.fired, c.now
	c.fired = nil
	c.mu.Unlock()
	for _, ch := range chans {
		ch <- now
	}
}

func (c *clock) requested() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.waits...)
}
