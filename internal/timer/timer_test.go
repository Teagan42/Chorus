package timer_test

import (
	"context"
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/announce"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
	"github.com/teaganglenn/chorus/internal/timer"
)

// patience bounds a wait that only expires when the implementation is wrong.
const patience = 5 * time.Second

// supper is when Alan puts the lasagne in, on a Thursday.
var supper = time.Date(2026, 10, 8, 18, 40, 0, 0, time.UTC)

// clock is a virtual clock whose timers fire only when the test advances it.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []vtimer
}

type vtimer struct {
	at time.Time
	ch chan time.Time
}

func newClock() *clock { return &clock{now: supper} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers = append(c.timers, vtimer{c.now.Add(d), ch})
	return ch
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var live []vtimer
	for _, t := range c.timers {
		if t.at.After(c.now) {
			live = append(live, t)
			continue
		}
		t.ch <- t.at
	}
	c.timers = live
	c.mu.Unlock()
}

// armed reports how many timers are waiting on the clock.
func (c *clock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// announcer stands in for the satellites: each name it holds is connected,
// and every announcement it is handed is kept.
type announcer struct {
	mu        sync.Mutex
	connected map[string]bool
	said      []said

	// unheard satellites take an announcement, then never play it.
	unheard map[string]bool
}

type said struct {
	satellite string
	a         session.Announcement
}

func newAnnouncer(connected ...string) *announcer {
	a := &announcer{connected: map[string]bool{}, unheard: map[string]bool{}}
	for _, s := range connected {
		a.connected[s] = true
	}
	return a
}

func (a *announcer) Announce(_ context.Context, satellite string, an session.Announcement) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.connected[satellite] {
		return "", announce.ErrNotConnected
	}
	if an.Heard != nil {
		an.Heard <- !a.unheard[satellite]
		an.Heard = nil
	}
	a.said = append(a.said, said{satellite, an})
	return "conv-announce-" + satellite, nil
}

// play sets whether satellite plays what it is handed.
func (a *announcer) play(satellite string, ok bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unheard[satellite] = !ok
}

func (a *announcer) connect(satellite string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.connected[satellite] = true
}

func (a *announcer) heard() []said {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]said(nil), a.said...)
}

func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", what)
}

// house is a scheduler over one store, which outlives it when a test
// restarts the daemon.
type house struct {
	store journal.Store
	clock *clock
	ann   *announcer
}

func newHouse(connected ...string) *house {
	return &house{store: journal.NewMemStore(), clock: newClock(), ann: newAnnouncer(connected...)}
}

// start is the daemon starting: a scheduler that lives until the test ends
// or stop is called.
func (h *house) start(t *testing.T) (*timer.Scheduler, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s, err := timer.Start(ctx, timer.Config{
		Journal: journal.New(h.store, h.clock, journal.Versions{}), Store: h.store,
		Clock: h.clock, Timers: h.clock, Announcer: h.ann,
	})
	if err != nil {
		cancel()
		t.Fatalf("start: %v", err)
	}
	stop := func() {
		cancel()
		s.Wait()
	}
	t.Cleanup(stop)
	return s, stop
}

func (h *house) log(t *testing.T) []journal.Event {
	t.Helper()
	events, err := h.store.Events(context.Background(), journal.HouseTimers)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func (h *house) finished(t *testing.T, n int) journal.Event {
	t.Helper()
	var got []journal.Event
	await(t, "a timer to go off", func() bool {
		got = got[:0]
		for _, e := range h.log(t) {
			if e.Kind == journal.KindTimerFinished {
				got = append(got, e)
			}
		}
		return len(got) >= n
	})
	return got[n-1]
}

var alanInTheKitchen = session.Caller{
	Person: "alan", ConversationID: "conv-1840-kitchen", CallID: "call_t1", Satellite: "kitchen",
}

// Alan sets the oven for twelve minutes in the kitchen. Twelve minutes later
// the kitchen says what the model asked it to, and the house log records
// that it did.
//
// verifies SPEC §4, §8
func TestATimerGoesOffWhereItWasSet(t *testing.T) {
	h := newHouse("kitchen", "office")
	s, _ := h.start(t)

	set, err := s.Set(context.Background(), alanInTheKitchen, timer.Request{
		Seconds: 720, Label: "oven", Announcement: "The lasagne is ready to come out.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !set.FiresAt.Equal(supper.Add(12 * time.Minute)) {
		t.Errorf("fires at %v, want 18:52", set.FiresAt)
	}

	h.clock.advance(11*time.Minute + 59*time.Second)
	if got := h.ann.heard(); len(got) != 0 {
		t.Fatalf("said %+v a second early", got)
	}
	h.clock.advance(time.Second)

	done := h.finished(t, 1)
	if done.Fields["outcome"] != "announced" || done.Fields["conversation_id"] != "conv-announce-kitchen" {
		t.Errorf("timer_finished = %v", done.Fields)
	}
	heard := h.ann.heard()
	if len(heard) != 1 || heard[0].satellite != "kitchen" {
		t.Fatalf("announced %+v, want once in the kitchen", heard)
	}
	want := session.Announcement{
		Text: "The lasagne is ready to come out.", Source: session.SourceTimer, TimerID: set.ID,
		RequestedBy: "alan", FromSatellite: "kitchen", FromConversation: "conv-1840-kitchen",
	}
	if heard[0].a != want {
		t.Errorf("announcement = %+v, want %+v", heard[0].a, want)
	}
	if got := s.Running(); len(got) != 0 {
		t.Errorf("still running after it went off: %+v", got)
	}
}

// Teagan sets the pasta, then changes her mind before it boils over. Nothing
// is said, and the log says it was cancelled by that call.
//
// verifies SPEC §8
func TestACancelledTimerSaysNothing(t *testing.T) {
	h := newHouse("kitchen")
	s, _ := h.start(t)
	teagan := session.Caller{Person: "teagan", ConversationID: "conv-1841-kitchen", CallID: "call_t2", Satellite: "kitchen"}

	pasta, err := s.Set(context.Background(), teagan, timer.Request{Seconds: 540, Label: "pasta"})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(4 * time.Minute)
	teagan.ConversationID, teagan.CallID = "conv-1844-kitchen", "call_t3"
	if _, err := s.Cancel(context.Background(), teagan, pasta.ID); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(10 * time.Minute)
	if _, err := s.Cancel(context.Background(), teagan, pasta.ID); err == nil {
		t.Error("cancelled the same timer twice")
	}
	if _, err := s.Cancel(context.Background(), teagan, "t_00000000"); err == nil {
		t.Error("cancelled a timer nobody set")
	}

	if got := h.ann.heard(); len(got) != 0 {
		t.Errorf("a cancelled timer said %+v", got)
	}
	last := h.log(t)[len(h.log(t))-1]
	if last.Kind != journal.KindTimerCancelled || last.Fields["call_id"] != "call_t3" || last.Fields["conversation_id"] != "conv-1844-kitchen" {
		t.Errorf("last event = %s %v", last.Kind, last.Fields)
	}
}

// The daemon restarts while the oven timer is running. The new one reads
// the house log, and the timer still goes off at 18:52, not twelve minutes
// after the restart.
//
// verifies SPEC §8
func TestATimerOutlivesTheDaemon(t *testing.T) {
	h := newHouse("kitchen")
	first, stop := h.start(t)
	if _, err := first.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 720, Label: "oven"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(5 * time.Minute)
	stop()

	h.clock.advance(time.Minute)
	second, _ := h.start(t)
	if got := second.Running(); len(got) != 1 || got[0].Label != "oven" {
		t.Fatalf("after the restart, running = %+v", got)
	}
	await(t, "the oven to be armed again", func() bool { return h.clock.armed() > 0 })
	h.clock.advance(6 * time.Minute)

	h.finished(t, 1)
	if got := h.ann.heard(); len(got) != 1 || got[0].a.Text != "The oven timer is done." {
		t.Errorf("announced %+v, want the oven's label said", got)
	}
}

// A timer that came due while the daemon was down goes off as soon as it is
// back, if that is within the grace. Past it, the moment is gone, and the
// log says it was missed rather than saying it at the wrong time.
//
// verifies SPEC §7, §8
func TestATimerThatCameDueWhileDownIsSaidLateOrMissed(t *testing.T) {
	cases := []struct {
		name    string
		down    time.Duration
		outcome string
		says    int
	}{
		{"back two minutes late", 14 * time.Minute, "announced", 1},
		{"back after supper", 3 * time.Hour, "missed", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHouse("kitchen")
			first, stop := h.start(t)
			if _, err := first.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 720, Label: "oven"}); err != nil {
				t.Fatal(err)
			}
			stop()
			h.clock.advance(c.down)

			h.start(t)
			done := h.finished(t, 1)
			if done.Fields["outcome"] != c.outcome {
				t.Errorf("outcome = %q (%s), want %s", done.Fields["outcome"], done.Fields["error"], c.outcome)
			}
			if got := h.ann.heard(); len(got) != c.says {
				t.Errorf("said %d times, want %d", len(got), c.says)
			}
		})
	}
}

// The kitchen satellite is rebooting when the oven goes off. The scheduler
// tries again until it is back, within the grace; a satellite that never
// returns leaves the timer unannounced, which triage reads as a failure.
//
// verifies SPEC §7
func TestATimerWaitsForItsSatelliteWithinTheGrace(t *testing.T) {
	h := newHouse()
	s, _ := h.start(t)
	if _, err := s.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 720, Label: "oven"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(12 * time.Minute)
	await(t, "a retry to be armed", func() bool { return h.clock.armed() > 0 })
	h.clock.advance(timer.RetryEvery)
	await(t, "another retry", func() bool { return h.clock.armed() > 0 })
	h.ann.connect("kitchen")
	h.clock.advance(timer.RetryEvery)
	if done := h.finished(t, 1); done.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v, want announced once the kitchen was back", done.Fields)
	}

	gone := newHouse()
	s, _ = gone.start(t)
	if _, err := s.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 60, Label: "tea"}); err != nil {
		t.Fatal(err)
	}
	gone.clock.advance(time.Minute)
	for range int(timer.DefaultGrace / timer.RetryEvery) {
		await(t, "a retry", func() bool { return gone.clock.armed() > 0 || len(gone.log(t)) == 2 })
		gone.clock.advance(timer.RetryEvery)
	}
	done := gone.finished(t, 1)
	if done.Fields["outcome"] != "unannounced" || !strings.Contains(done.Fields["error"], "kitchen: not connected") {
		t.Errorf("timer_finished = %v, want unannounced on a kitchen that never came back", done.Fields)
	}
}

// The oven goes off as the kitchen's link drops: the announcement is taken,
// then never played. Queued is not heard, so the scheduler says it again
// once the kitchen plays again, and a kitchen that never does leaves the
// timer unannounced rather than announced.
//
// verifies SPEC §7
func TestATimerQueuedButNeverPlayedIsSaidAgain(t *testing.T) {
	h := newHouse("kitchen")
	h.ann.play("kitchen", false)
	s, _ := h.start(t)
	if _, err := s.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 720, Label: "oven"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(12 * time.Minute)
	await(t, "a retry to be armed", func() bool { return h.clock.armed() > 0 })
	h.ann.play("kitchen", true)
	h.clock.advance(timer.RetryEvery)
	if done := h.finished(t, 1); done.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v, want announced once the kitchen played it", done.Fields)
	}
	if got := h.ann.heard(); len(got) != 2 {
		t.Errorf("handed to the kitchen %d times, want twice", len(got))
	}

	gone := newHouse("kitchen")
	gone.ann.play("kitchen", false)
	s, _ = gone.start(t)
	if _, err := s.Set(context.Background(), alanInTheKitchen, timer.Request{Seconds: 60, Label: "tea"}); err != nil {
		t.Fatal(err)
	}
	gone.clock.advance(time.Minute)
	for range int(timer.DefaultGrace / timer.RetryEvery) {
		await(t, "a retry", func() bool { return gone.clock.armed() > 0 || len(gone.log(t)) == 2 })
		gone.clock.advance(timer.RetryEvery)
	}
	done := gone.finished(t, 1)
	if done.Fields["outcome"] != "unannounced" || !strings.Contains(done.Fields["error"], "kitchen: not heard") {
		t.Errorf("timer_finished = %v, want unannounced on a kitchen that never played it", done.Fields)
	}
}

// What the model may ask for is bounded: a timer has to run, has to have a
// satellite to go off on, and is not a reminder for next week.
//
// verifies SPEC §7
func TestATimerTheHouseCannotKeepIsRefused(t *testing.T) {
	h := newHouse("kitchen")
	s, _ := h.start(t)
	nowhere := alanInTheKitchen
	nowhere.Satellite = ""
	for name, c := range map[string]struct {
		caller session.Caller
		secs   int
	}{
		"no time at all":   {alanInTheKitchen, 0},
		"backwards":        {alanInTheKitchen, -60},
		"a week":           {alanInTheKitchen, int((7 * 24 * time.Hour).Seconds())},
		"set from nowhere": {nowhere, 600},
	} {
		if _, err := s.Set(context.Background(), c.caller, timer.Request{Seconds: c.secs, Label: "oven"}); err == nil {
			t.Errorf("%s: set", name)
		}
	}
	if got := h.log(t); len(got) != 0 {
		t.Errorf("a refused timer was logged: %+v", got)
	}
}

// The tools are what the model sees: an id to cancel by, and seconds left
// rather than a UTC instant it would have to convert.
//
// verifies SPEC §6
func TestTheTimerToolsAnswerTheModel(t *testing.T) {
	h := newHouse("kitchen")
	s, _ := h.start(t)
	tools := timer.Tools(s)
	ctx := session.WithCaller(context.Background(), alanInTheKitchen)

	out, err := tools["timer_start"].Invoke(ctx, `{"seconds":720,"label":"oven","announcement":"The oven timer is done."}`)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		TimerID     string `json:"timer_id"`
		Label       string `json:"label"`
		Satellite   string `json:"satellite"`
		SecondsLeft int    `json:"seconds_left"`
		Says        string `json:"says"`
	}
	if err := json.Unmarshal([]byte(out), &started); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if !strings.HasPrefix(started.TimerID, "t_") || started.SecondsLeft != 720 || started.Says != "The oven timer is done." || started.Satellite != "kitchen" {
		t.Errorf("timer_start = %s", out)
	}

	h.clock.advance(97 * time.Second)
	out, err = tools["timer_list"].Invoke(ctx, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"timers":[{"timer_id":"` + started.TimerID + `","label":"oven","satellite":"kitchen","seconds_left":623}]}`
	if out != want {
		t.Errorf("timer_list = %s, want %s", out, want)
	}

	if _, err := tools["timer_cancel"].Invoke(ctx, `{"timer_id":"t_deadbeef"}`); err == nil || !strings.Contains(err.Error(), "t_deadbeef") {
		t.Errorf("cancelling a timer nobody set: %v", err)
	}
	out, err = tools["timer_cancel"].Invoke(ctx, `{"timer_id":" `+started.TimerID+` "}`)
	if err != nil || out != `{"cancelled":"`+started.TimerID+`","label":"oven"}` {
		t.Errorf("timer_cancel = %s, %v", out, err)
	}
	if out, _ := tools["timer_list"].Invoke(ctx, `{}`); out != `{"timers":[]}` {
		t.Errorf("timer_list after the cancel = %s", out)
	}
	if _, err := tools["timer_start"].Invoke(ctx, `{"seconds":"twelve minutes"}`); err == nil {
		t.Error("a duration in words was taken as seconds")
	}
}

// Says is the timer's own words, the label's, or a plain fallback.
func TestWhatATimerSays(t *testing.T) {
	for want, tm := range map[string]journal.Timer{
		"Take the bread out.":      {Label: "bread", Announcement: "Take the bread out."},
		"The pasta timer is done.": {Label: "pasta"},
		"Your timer is done.":      {},
	} {
		if got := timer.Says(tm); got != want {
			t.Errorf("Says(%+v) = %q, want %q", tm, got, want)
		}
	}
}

// A scheduler with nothing to say it through cannot keep a timer.
func TestStartRequiresItsWiring(t *testing.T) {
	store := journal.NewMemStore()
	clk := newClock()
	if _, err := timer.Start(context.Background(), timer.Config{
		Journal: journal.New(store, clk, journal.Versions{}), Store: store, Clock: clk, Timers: clk,
	}); err == nil {
		t.Error("started with no announcer")
	}
	var corrupt journal.Store = brokenStore{store}
	if _, err := timer.Start(context.Background(), timer.Config{
		Journal: journal.New(corrupt, clk, journal.Versions{}), Store: corrupt, Clock: clk, Timers: clk,
		Announcer: newAnnouncer(),
	}); err == nil {
		t.Error("started over a house log it could not read")
	}
}

type brokenStore struct{ journal.Store }

func (brokenStore) Events(context.Context, string) ([]journal.Event, error) {
	return nil, errors.New("connection refused")
}
