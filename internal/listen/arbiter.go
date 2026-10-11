package listen

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// DefaultArbitration is how long a wake waits for the same wake from the
// house's other satellites: wider than Wi-Fi jitter and the spread between
// two devices' detections, and hidden behind the words that follow it (ADR-0072).
const DefaultArbitration = 200 * time.Millisecond

// DefaultArbitrationSpan is the processed audio before a wake frame its
// level is measured over: 600 ms, about the length of "Hey Eddie".
const DefaultArbitrationSpan = 600 * bytesPerSecond / 1000

// Verdict is how one contest ended for one satellite in it.
type Verdict struct {
	// Winner is the satellite that answers; Won is whether that is this one.
	Winner string
	Won    bool

	// Energy and WinnerEnergy are each side's level over the stretch before
	// its wake frame, as a fraction of full scale (RMS).
	Energy, WinnerEnergy float64
}

// Entry is one satellite's wake, as it is entered in a contest.
type Entry struct {
	Satellite string
	// Energy is the processed channel's RMS over the stretch before the
	// wake frame, as a fraction of full scale.
	Energy float64
	// Pressed is a push-to-talk press (bridge.WakeButton): unambiguous about
	// which device was meant, so it beats any spoken wake.
	Pressed bool
}

// Contender decides which satellite answers a wake several heard.
// *Arbiter fills it.
type Contender interface {
	// Contend enters a wake. The channel receives one verdict, ready at
	// once when there is nobody to wait for.
	Contend(e Entry) <-chan Verdict
}

// ArbiterConfig wires an Arbiter. The clock and the timers are injected,
// so a contest is decided in tests without a wall clock (CONTRIBUTING §1).
type ArbiterConfig struct {
	// Window is how long a contest stays open after its first wake.
	Window time.Duration
	Clock  journal.Clock
	Timers session.Timers

	// Satellites is the inventory, in its order: the last tie-break.
	Satellites []string
}

// Arbiter holds the house's one open contest. A wake arriving within the
// window of the contest's first joins it; any later one opens the next.
// Times are arrival on the host's clock, never the device's.
type Arbiter struct {
	cfg  ArbiterConfig
	ctx  context.Context
	rank map[string]int

	mu      sync.Mutex
	present map[string]bool
	cur     *round
}

// round is one contest.
type round struct {
	opened  time.Time
	entries []*entry
	decided bool
	over    chan struct{}
}

// entry is one satellite's wake in a round.
type entry struct {
	satellite string
	energy    float64
	pressed   bool
	at        time.Time
	outs      []chan Verdict
}

// NewArbiter validates the wiring. Its timers run until ctx ends.
func NewArbiter(ctx context.Context, cfg ArbiterConfig) (*Arbiter, error) {
	switch {
	case cfg.Window <= 0:
		return nil, errors.New("listen: an arbiter needs a window; zero means no arbiter")
	case cfg.Clock == nil:
		return nil, errors.New("listen: an arbiter needs a clock")
	case cfg.Timers == nil:
		return nil, errors.New("listen: an arbiter needs timers")
	}
	rank := make(map[string]int, len(cfg.Satellites))
	for i, s := range cfg.Satellites {
		rank[s] = i
	}
	return &Arbiter{cfg: cfg, ctx: ctx, rank: rank, present: map[string]bool{}}, nil
}

// Present says a satellite's audio link came up or went. Only a satellite
// that is linked can enter a contest, so only those are waited for.
func (a *Arbiter) Present(satellite string, up bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if up {
		a.present[satellite] = true
		return
	}
	delete(a.present, satellite)
	if c := a.cur; c != nil && !c.decided && a.allInLocked(c) {
		a.decideLocked(c)
	}
}

// Contend enters a wake (Contender).
func (a *Arbiter) Contend(w Entry) <-chan Verdict {
	out := make(chan Verdict, 1)
	now := a.cfg.Clock.Now()
	a.mu.Lock()
	defer a.mu.Unlock()
	c, fresh := a.cur, false
	if c == nil || c.decided || now.Sub(c.opened) >= a.cfg.Window {
		c, fresh = &round{opened: now, over: make(chan struct{})}, true
		a.cur = c
	}
	if i := slices.IndexFunc(c.entries, func(e *entry) bool { return e.satellite == w.Satellite }); i >= 0 {
		// A link that reconnected mid-contest: one satellite, one entry.
		e := c.entries[i]
		e.energy, e.pressed, e.outs = max(e.energy, w.Energy), e.pressed || w.Pressed, append(e.outs, out)
	} else {
		c.entries = append(c.entries, &entry{
			satellite: w.Satellite, energy: w.Energy, pressed: w.Pressed, at: now, outs: []chan Verdict{out},
		})
	}
	switch {
	case a.allInLocked(c):
		a.decideLocked(c)
	case fresh:
		go a.expire(c)
	}
	return out
}

// expire decides c when its window ends, unless everyone entered first.
func (a *Arbiter) expire(c *round) {
	select {
	case <-a.cfg.Timers.After(a.cfg.Window):
	case <-c.over:
		return
	case <-a.ctx.Done():
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !c.decided {
		a.decideLocked(c)
	}
}

// allInLocked reports that every linked satellite has entered c, so nobody
// is left to wait for.
func (a *Arbiter) allInLocked(c *round) bool {
	for s := range a.present {
		if !slices.ContainsFunc(c.entries, func(e *entry) bool { return e.satellite == s }) {
			return false
		}
	}
	return true
}

// decideLocked picks c's winner and tells every entry.
func (a *Arbiter) decideLocked(c *round) {
	c.decided = true
	close(c.over)
	w := slices.MinFunc(c.entries, a.better)
	for _, e := range c.entries {
		v := Verdict{Winner: w.satellite, Won: e == w, Energy: e.energy, WinnerEnergy: w.energy}
		for _, out := range e.outs {
			out <- v
		}
	}
}

// better orders entries best first: a press, then the loudest, then the
// earliest wake, then the inventory's order, then the name, so no tie is
// left to chance.
func (a *Arbiter) better(x, y *entry) int {
	return cmp.Or(
		pressedFirst(x, y),
		cmp.Compare(y.energy, x.energy),
		x.at.Compare(y.at),
		cmp.Compare(a.rankOf(x.satellite), a.rankOf(y.satellite)),
		cmp.Compare(x.satellite, y.satellite),
	)
}

// pressedFirst orders a press before a spoken wake.
func pressedFirst(x, y *entry) int {
	switch {
	case x.pressed == y.pressed:
		return 0
	case x.pressed:
		return -1
	}
	return 1
}

// rankOf is a satellite's place in the inventory; one it does not name
// comes after all that it does.
func (a *Arbiter) rankOf(s string) int {
	if r, ok := a.rank[s]; ok {
		return r
	}
	return len(a.rank)
}
