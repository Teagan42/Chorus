package listen_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/listen"
)

// The house the arbiter decides for, in inventory order.
var house = []string{"kitchen", "office", "living_room"}

// Seven forty-two on a weekday morning, when Teagan asks for the kitchen lights.
var breakfast = time.Date(2026, 10, 8, 7, 42, 0, 0, time.UTC)

// vclock is a clock whose time and timers move only when the test says so.
type vclock struct {
	mu     sync.Mutex
	now    time.Time
	timers []vtimer
}

type vtimer struct {
	due time.Time
	d   time.Duration
	ch  chan time.Time
}

func newVClock() *vclock { return &vclock{now: breakfast} }

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *vclock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, vtimer{due: c.now.Add(d), d: d, ch: ch})
	return ch
}

// advance moves the clock, firing every timer that came due.
func (c *vclock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var live []vtimer
	for _, t := range c.timers {
		if t.due.After(c.now) {
			live = append(live, t)
			continue
		}
		t.ch <- t.due
	}
	c.timers = live
	c.mu.Unlock()
}

// armed is how many timers of exactly d are waiting.
func (c *vclock) armed(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.d == d {
			n++
		}
	}
	return n
}

const window = listen.DefaultArbitration

// newArbiter is the arbiter with every satellite named in present linked.
func newArbiter(t *testing.T, clk *vclock, present ...string) *listen.Arbiter {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a, err := listen.NewArbiter(ctx, listen.ArbiterConfig{Window: window, Clock: clk, Timers: clk, Satellites: house})
	if err != nil {
		t.Fatalf("arbiter: %v", err)
	}
	for _, s := range present {
		a.Present(s, true)
	}
	return a
}

// verdict is the decision on ch, which must already have been made.
func verdict(t *testing.T, ch <-chan listen.Verdict, who string) listen.Verdict {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(patience):
		t.Fatalf("%s was never told how the contest ended", who)
		return listen.Verdict{}
	}
}

// undecided fails if ch already holds a verdict.
func undecided(t *testing.T, ch <-chan listen.Verdict, who string) {
	t.Helper()
	select {
	case v := <-ch:
		t.Fatalf("%s was decided too soon: %+v", who, v)
	default:
	}
}

// Teagan says the wake word at the kitchen counter. The kitchen hears it
// loud, the office down the hall hears it faint. Once both have said so
// there is nobody left to wait for, so the clock never moves: the kitchen
// wins, and the office is told it lost to the kitchen, with both levels
// (SPEC §4.5).
//
// verifies SPEC §4.5
func TestTheLouderSatelliteWinsTheWake(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	undecided(t, kitchen, "the kitchen")
	office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.04})

	k, o := verdict(t, kitchen, "the kitchen"), verdict(t, office, "the office")
	if !k.Won || k.Winner != "kitchen" {
		t.Errorf("kitchen = %+v, want it to win", k)
	}
	if o.Won || o.Winner != "kitchen" || o.Energy != 0.04 || o.WinnerEnergy != 0.30 {
		t.Errorf("office = %+v, want it lost to the kitchen at 0.04 against 0.30", o)
	}
}

// A satellite that is the only one linked has nobody to wait for: the
// wake is answered without arming the window at all (SPEC §11).
//
// verifies SPEC §4.5, §11
func TestALoneSatelliteIsAnsweredAtOnce(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen")

	v := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.02})
	select {
	case got := <-v:
		if !got.Won || got.Winner != "kitchen" {
			t.Errorf("verdict = %+v, want the kitchen to win its own wake", got)
		}
	default:
		t.Fatal("a lone satellite's wake waited for a contest nobody else can enter")
	}
	if n := clk.armed(window); n != 0 {
		t.Errorf("%d window timers armed for a lone satellite", n)
	}
}

// Teagan wakes the kitchen while the office is linked and hears nothing.
// The kitchen waits out the window on the injected clock and not a
// moment longer, then wins alone (SPEC §4.5, §11).
//
// verifies SPEC §4.5, §11
func TestTheWindowExpiresOnTheClock(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	await(t, "the window's timer", func() bool { return clk.armed(window) == 1 })
	clk.advance(window - time.Millisecond)
	undecided(t, kitchen, "the kitchen")
	clk.advance(time.Millisecond)
	if v := verdict(t, kitchen, "the kitchen"); !v.Won || v.Winner != "kitchen" {
		t.Errorf("kitchen = %+v, want it to win alone", v)
	}
}

// Two satellites hear Alice equally. The one whose wake frame arrived
// first wins; with the frames arriving together, the inventory's order
// decides. Never a coin toss: a replay of the log must say the same.
//
// verifies SPEC §4.5
func TestATieGoesToTheEarlierWakeThenTheInventory(t *testing.T) {
	t.Run("earlier wake", func(t *testing.T) {
		clk := newVClock()
		a := newArbiter(t, clk, "kitchen", "office", "living_room")
		office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.12})
		clk.advance(40 * time.Millisecond)
		kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.12})
		await(t, "the window's timer", func() bool { return clk.armed(window) == 1 })
		clk.advance(window)
		if v := verdict(t, office, "the office"); !v.Won {
			t.Errorf("office = %+v, want the earlier wake to win the tie", v)
		}
		if v := verdict(t, kitchen, "the kitchen"); v.Won || v.Winner != "office" {
			t.Errorf("kitchen = %+v, want it lost to the office", v)
		}
	})
	t.Run("inventory order", func(t *testing.T) {
		clk := newVClock()
		a := newArbiter(t, clk, "kitchen", "office", "living_room")
		living := a.Contend(listen.Entry{Satellite: "living_room", Energy: 0.12})
		office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.12})
		kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.12})
		if v := verdict(t, kitchen, "the kitchen"); !v.Won {
			t.Errorf("kitchen = %+v, want the inventory's first to win a dead heat", v)
		}
		for who, ch := range map[string]<-chan listen.Verdict{"office": office, "living_room": living} {
			if v := verdict(t, ch, who); v.Won || v.Winner != "kitchen" {
				t.Errorf("%s = %+v, want it lost to the kitchen", who, v)
			}
		}
	})
}

// The office's wake frame arrives after the kitchen's window has closed.
// It is not the same wake any more: it starts a contest of its own and
// wins it, rather than losing to a kitchen that has already answered.
// A deliberate re-wake on another satellite is migration's (SPEC §4.5).
//
// verifies SPEC §4.5
func TestAWakeAfterTheWindowStartsANewContest(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	await(t, "the kitchen's window", func() bool { return clk.armed(window) == 1 })
	clk.advance(window)
	if v := verdict(t, kitchen, "the kitchen"); !v.Won {
		t.Fatalf("kitchen = %+v, want it to win alone", v)
	}

	office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.04})
	undecided(t, office, "the office")
	await(t, "the office's window", func() bool { return clk.armed(window) == 1 })
	clk.advance(window)
	if v := verdict(t, office, "the office"); !v.Won || v.Winner != "office" {
		t.Errorf("office = %+v, want a contest of its own, won", v)
	}
}

// The window is measured on arrival at the host, not on a timer that may
// not have been read yet: a frame arriving once the window has passed on
// the clock opens a new contest even before the old one is decided.
//
// verifies SPEC §4.5
func TestArrivalPastTheWindowIsANewContestEvenBeforeTheTimerIsRead(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office", "living_room")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	await(t, "the kitchen's window", func() bool { return clk.armed(window) == 1 })
	clk.mu.Lock()
	clk.now = clk.now.Add(window) // time passes; the timer has not fired
	clk.mu.Unlock()
	office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.04})

	clk.advance(0)
	if v := verdict(t, kitchen, "the kitchen"); !v.Won {
		t.Errorf("kitchen = %+v, want it to win the first contest", v)
	}
	undecided(t, office, "the office")
	await(t, "the office's window", func() bool { return clk.armed(window) == 1 })
	clk.advance(window)
	if v := verdict(t, office, "the office"); !v.Won {
		t.Errorf("office = %+v, want it to win a contest of its own", v)
	}
}

// The office's link drops while the kitchen waits on it: nobody is left to
// enter, so the kitchen is answered then rather than at the window's end.
//
// verifies SPEC §4.5
func TestASatelliteThatGoesAwayEndsTheWait(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	undecided(t, kitchen, "the kitchen")
	a.Present("office", false)
	if v := verdict(t, kitchen, "the kitchen"); !v.Won {
		t.Errorf("kitchen = %+v, want it to win once the office left", v)
	}
}

// A zero window is no arbitration, and the composition root builds no
// arbiter for it: asking for one is a wiring mistake.
func TestAnArbiterNeedsAWindowAndAClock(t *testing.T) {
	clk := newVClock()
	for name, cfg := range map[string]listen.ArbiterConfig{
		"no window": {Clock: clk, Timers: clk},
		"no clock":  {Window: window, Timers: clk},
		"no timers": {Window: window, Clock: clk},
	} {
		if _, err := listen.NewArbiter(context.Background(), cfg); err == nil {
			t.Errorf("%s: built an arbiter", name)
		}
	}
}

// Bob presses the office Voice PE's button while Teagan, nearer the
// kitchen, is talking loud enough to wake it. The press is unambiguous
// about which device was meant, so the office wins however faint it is.
//
// verifies SPEC §4.5
func TestAPressBeatsALouderVoice(t *testing.T) {
	clk := newVClock()
	a := newArbiter(t, clk, "kitchen", "office")

	kitchen := a.Contend(listen.Entry{Satellite: "kitchen", Energy: 0.30})
	office := a.Contend(listen.Entry{Satellite: "office", Energy: 0.01, Pressed: true})
	if v := verdict(t, office, "the office"); !v.Won {
		t.Errorf("office = %+v, want the press to win", v)
	}
	if v := verdict(t, kitchen, "the kitchen"); v.Won || v.Winner != "office" {
		t.Errorf("kitchen = %+v, want it lost to the press", v)
	}
}
