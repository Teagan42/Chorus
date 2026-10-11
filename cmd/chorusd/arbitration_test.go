package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

// quietHousehold is Alan, Teagan and Alice's house with a model that only
// listens, so the rig's engine is the one their asks reach.
func quietHousehold(t *testing.T, tweak ...func(*deps)) *rig {
	t.Helper()
	eng := &scriptEngine{acts: []session.Action{session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r := newHousehold(t, eng, tweak...)
	r.engine = eng
	return r
}

// window is the arbitration window chorusd runs with unless told otherwise.
const window = listen.DefaultArbitration

// How loud "Hey Eddie" lands on each satellite: Teagan at the kitchen
// counter, the office down the hall.
const (
	nearby  int16 = 9000
	acrossT int16 = 1500
)

// arbitrating runs the daemon with wake arbitration on and time moving only
// when the test moves clk.
func arbitrating(clk *clock) func(*deps) {
	return func(d *deps) {
		d.WakeArbitration = window
		d.Clock, d.Timers = clk, clk
	}
}

// sayWakeWord is "Hey Eddie" at a satellite, as loud as it lands there,
// and the wake frame its micro_wake_word sends after it.
func sayWakeWord(t *testing.T, dev *bridgetest.Device, amplitude int16) {
	t.Helper()
	for sent := 0; sent < listen.DefaultArbitrationSpan; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, voice(amplitude, chunkBytes))
	}
	dev.SendWake(t, "hey_eddie")
}

// settle proves the device's earlier frames were dispatched: the next frame
// it reads rides a channel no listener keeps.
func settle(t *testing.T, dev *bridgetest.Device) {
	t.Helper()
	dev.SendMic(t, bridge.ChannelSecond+1, quiet(2))
}

// armed reports a timer of exactly d waiting on the clock.
func (c *clock) armed(d time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.timers, func(t *vtimer) bool { return t.waited == d })
}

// heardOnce asserts the whole house answered one ask, on the kitchen, and
// that the office's log says it lost to the kitchen.
func heardOnce(t *testing.T, r *rig, office *bridgetest.Device, text, speaker string) {
	t.Helper()
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "kitchen" || opened.Fields["speaker_id"] != speaker {
		t.Errorf("session_opened = %v, want the kitchen for %q", opened.Fields, speaker)
	}
	rej := r.store.awaitKind(t, journal.KindWakeRejected, 1)
	if rej.ConversationID != listen.DeviceConversation("office") || rej.Fields["reason"] != "arbitrated" ||
		rej.Fields["lost_to"] != "kitchen" || rej.Fields["wake_word"] != "hey_eddie" {
		t.Errorf("wake_rejected in %s = %v, want the office's wake lost to the kitchen", rej.ConversationID, rej.Fields)
	}
	e, errE := strconv.ParseFloat(rej.Fields["energy_dbfs"], 64)
	w, errW := strconv.ParseFloat(rej.Fields["winner_energy_dbfs"], 64)
	if errE != nil || errW != nil || e >= w {
		t.Errorf("levels %q against %q dBFS, want the office fainter", rej.Fields["energy_dbfs"], rej.Fields["winner_energy_dbfs"])
	}
	await(t, "the kitchen's turn", func() bool { return len(r.engine.heard()) >= 1 })
	settle(t, office)
	if got := askedTexts(r.engine); !slices.Equal(got, []string{text}) {
		t.Errorf("the model was asked %q, want %q once", got, text)
	}
	if n := len(r.store.ofKind(journal.KindSessionOpened)); n != 1 {
		t.Errorf("%d sessions opened, want only the kitchen's", n)
	}
	for _, e := range r.store.ofKind(journal.KindUtteranceTranscribed) {
		if e.ConversationID != opened.ConversationID {
			t.Errorf("an utterance was transcribed outside the kitchen's conversation: %v", e.Fields)
		}
	}
}

// bothHear is the one ask, said at the kitchen counter: the kitchen hears
// it loud and the office hears it faint, both after their own wake frame.
func bothHear(t *testing.T, r *rig, kitchen, office *bridgetest.Device, text string, who []float32) {
	t.Helper()
	near, far := r.line(text, who), r.line(text, who)
	sayWakeWord(t, kitchen, nearby)
	sayWakeWord(t, office, acrossT)
	r.utter(t, kitchen, near)
	r.utter(t, office, far)
}

// Teagan says "Hey Eddie, turn on the kitchen lights" at the kitchen
// counter, and the office down the hall hears it too. Only the kitchen
// answers; the office stays dark and silent, and its log says it lost to
// the kitchen. Both satellites entered, so nobody waits out the window:
// the clock never moves (SPEC §4.5, §11).
//
// verifies SPEC §4.5, §11
func TestOnlyTheNearerSatelliteAnswersTeagan(t *testing.T) {
	clk := newClock()
	r := quietHousehold(t, arbitrating(clk))
	kitchen, office := r.join(t, kitchenIP), r.join(t, officeIP)

	bothHear(t, r, kitchen, office, "turn on the kitchen lights", teagan)
	heardOnce(t, r, office, "turn on the kitchen lights", "teagan")
	if opened := r.store.ofKind(journal.KindSessionOpened)[0]; !opened.At.Equal(epoch) {
		t.Errorf("the session opened at %v, want %v: nobody was left to wait for", opened.At, epoch)
	}
}

// nobodyEnrolled is a household whose speaker ID runs with no voiceprints
// yet: every voice is a guest who may open a session (SPEC §5).
func nobodyEnrolled(t *testing.T, emb *fakeEmbedder) func(*deps) {
	return func(d *deps) {
		r, err := identity.NewResolver(emb, identity.New(emb), identity.Thresholds{})
		if err != nil {
			t.Fatalf("resolver: %v", err)
		}
		d.speakers, d.household = r, nil
	}
}

// A friend house-sitting before anyone has enrolled asks for the porch
// light from the kitchen. A guest's conversation is never resumed, so
// without arbitration the office opened a second one and answered too;
// with it, only the kitchen answers (SPEC §4.5, §5).
//
// verifies SPEC §4.5, §5
func TestAGuestIsAnsweredByOneSatellite(t *testing.T) {
	clk := newClock()
	emb := newEmbedder()
	r := newRig(t, inventory(), nobodyEnrolled(t, emb), arbitrating(clk))
	r.emb = emb // lines are scripted into the embedder the resolver asks
	kitchen, office := r.join(t, kitchenIP), r.join(t, officeIP)

	bothHear(t, r, kitchen, office, "turn on the porch light", stranger)
	heardOnce(t, r, office, "turn on the porch light", "")
}

// speakerIDOff is a household that runs no speaker identification: every
// voice is a guest (ADR-0031).
func speakerIDOff(d *deps) { d.speakers, d.household = nil, nil }

// The same ask in a house with speaker identification off is answered once.
//
// verifies SPEC §4.5, §5
func TestWithSpeakerIDOffOneSatelliteAnswers(t *testing.T) {
	clk := newClock()
	r := newRig(t, inventory(), speakerIDOff, arbitrating(clk))
	kitchen, office := r.join(t, kitchenIP), r.join(t, officeIP)

	bothHear(t, r, kitchen, office, "turn on the porch light", stranger)
	heardOnce(t, r, office, "turn on the porch light", "")
}

// With CHORUS_WAKE_ARBITRATION_MS=0 the house behaves as it did before
// arbitration: both satellites that heard the guest open a session and
// answer. This is the double answer arbitration exists to prevent.
//
// verifies SPEC §4.5
func TestWithArbitrationOffBothSatellitesAnswer(t *testing.T) {
	r := newRig(t, inventory(), speakerIDOff)
	kitchen, office := r.join(t, kitchenIP), r.join(t, officeIP)

	bothHear(t, r, kitchen, office, "turn on the porch light", stranger)
	r.store.awaitKind(t, journal.KindSessionOpened, 2)
	await(t, "both turns", func() bool { return len(r.engine.heard()) == 2 })
	if n := len(r.store.ofKind(journal.KindWakeRejected)); n != 0 {
		t.Errorf("%d rejections with arbitration off", n)
	}
}

// The kitchen is the only satellite linked when Teagan wakes it, so there
// is nobody to contend with: the session opens without the window ever
// being armed, and without the clock moving (SPEC §11).
//
// verifies SPEC §4.5, §11
func TestALoneSatelliteIsNotDelayed(t *testing.T) {
	clk := newClock()
	r := quietHousehold(t, arbitrating(clk))
	kitchen := r.join(t, kitchenIP)

	sayWakeWord(t, kitchen, nearby)
	r.utter(t, kitchen, r.line("what time is it", teagan))
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if !opened.At.Equal(epoch) || clk.armed(window) {
		t.Errorf("the session opened at %v with the window armed=%v, want %v and never armed", opened.At, clk.armed(window), epoch)
	}
}

// Teagan wakes the kitchen while the office is linked and hears nothing.
// Sent in one burst, as the rig does, the ask has fully arrived before the
// window ends, so the session opens exactly one window later: the worst
// the window can cost, and the bound the fast path is measured against
// (SPEC §11).
//
// verifies SPEC §4.5, §11
func TestAWakeNobodyElseHeardWaitsOneWindowAtMost(t *testing.T) {
	clk := newClock()
	r := quietHousehold(t, arbitrating(clk))
	kitchen := r.join(t, kitchenIP)
	r.join(t, officeIP)

	sayWakeWord(t, kitchen, nearby)
	r.utter(t, kitchen, r.line("what time is it", teagan))
	clk.awaitWait(t, window)
	if n := len(r.store.ofKind(journal.KindSessionOpened)); n != 0 {
		t.Fatalf("%d sessions opened before the window ended", n)
	}
	clk.advance(window)
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	if got := opened.At.Sub(epoch); got != window {
		t.Errorf("the session opened %v after the ask arrived, want exactly %v", got, window)
	}
	if opened.Fields["satellite"] != "kitchen" {
		t.Errorf("session_opened = %v, want the kitchen", opened.Fields)
	}
}

// pacedAsk is "what time is it" after the wake, streamed in real time: the
// clock moves 32 ms before every chunk, as the mic's own send cadence does,
// and stops on the chunk the endpointer ends the ask on, so whatever the
// session journals next is stamped with that instant.
func pacedAsk(t *testing.T, r *rig, clk *clock, dev *bridgetest.Device, amp int16, arbitrated bool) {
	t.Helper()
	const chunk = 32 * time.Millisecond
	var sent time.Duration
	decided := false
	send := func(pcm []byte) {
		clk.advance(chunk)
		sent += chunk
		if arbitrated && !decided && sent >= window {
			// The verdict lands while Teagan is still talking.
			r.logs.await(t, "wake arbitration decided")
			decided = true
		}
		dev.SendMic(t, bridge.ChannelAEC, pcm)
	}
	for range 8 {
		send(voice(amp, chunkBytes))
	}
	for n := 0; n < listen.DefaultSilence; n += chunkBytes {
		send(quiet(chunkBytes))
	}
}

// The same lone wake, with the ask streamed as Teagan speaks it. The
// window runs out while she is still talking, so the session opens at the
// same instant as with arbitration off: on the path from her last word to
// the answer, the window costs nothing (SPEC §11).
//
// verifies SPEC §11
func TestAWindowShorterThanTheAskCostsNothing(t *testing.T) {
	openedAt := map[bool]time.Duration{}
	for _, on := range []bool{false, true} {
		clk := newClock()
		tweak := func(d *deps) { d.Clock, d.Timers = clk, clk }
		if on {
			tweak = arbitrating(clk)
		}
		r := quietHousehold(t, tweak)
		kitchen := r.join(t, kitchenIP)
		r.join(t, officeIP)

		sayWakeWord(t, kitchen, nearby)
		if on {
			clk.awaitWait(t, window)
		}
		pacedAsk(t, r, clk, kitchen, r.line("what time is it", teagan), on)
		openedAt[on] = r.store.awaitKind(t, journal.KindSessionOpened, 1).At.Sub(epoch)
	}
	t.Logf("session opened %v after the wake, arbitration on; %v off", openedAt[true], openedAt[false])
	if openedAt[true] != openedAt[false] {
		t.Errorf("the session opened %v after the wake with arbitration, %v without: want no difference",
			openedAt[true], openedAt[false])
	}
}

// Teagan asks the kitchen something, then walks to the office and wakes it
// on purpose a little later. That is no simultaneous wake but migration:
// the office takes her conversation and the kitchen's session ends as
// migrated, as it did before arbitration (SPEC §4.5, ADR-0022).
//
// verifies SPEC §4.5
func TestADeliberateReWakeElsewhereStillMigrates(t *testing.T) {
	clk := newClock()
	r := quietHousehold(t, arbitrating(clk))
	kitchen, office := r.join(t, kitchenIP), r.join(t, officeIP)

	sayWakeWord(t, kitchen, nearby)
	r.utter(t, kitchen, r.line("what's on the calendar today", teagan))
	clk.awaitWait(t, window)
	clk.advance(window)
	first := r.store.awaitKind(t, journal.KindSessionOpened, 1)
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 1)

	clk.advance(5 * time.Second)
	sayWakeWord(t, office, nearby)
	r.utter(t, office, r.line("and tomorrow", teagan))
	clk.awaitWait(t, window)
	clk.advance(window)
	second := r.store.awaitKind(t, journal.KindSessionOpened, 2)
	closed := r.store.awaitKind(t, journal.KindSessionClosed, 1)
	if second.Fields["satellite"] != "office" || second.Fields["resumed"] != "true" || second.ConversationID != first.ConversationID {
		t.Errorf("second open = %v in %s, want the office resuming %s", second.Fields, second.ConversationID, first.ConversationID)
	}
	if closed.Fields["reason"] != "migrated" || closed.Fields["satellite"] != "kitchen" {
		t.Errorf("session_closed = %v, want the kitchen migrated", closed.Fields)
	}
	if n := len(r.store.ofKind(journal.KindWakeRejected)); n != 0 {
		t.Errorf("%d rejections for a deliberate re-wake", n)
	}
}

// The window is the daemon's to configure: 200 ms unless told otherwise,
// 0 turns arbitration off, and anything else that is not a whole number of
// milliseconds fails the start naming the variable (SPEC §13).
//
// verifies SPEC §13
func TestTheArbitrationWindowIsConfigured(t *testing.T) {
	got, err := configFromEnv(lookup(complete())).wakeArbitration()
	if err != nil || got != listen.DefaultArbitration {
		t.Errorf("default = %v, %v; want %v", got, err, listen.DefaultArbitration)
	}
	env := complete()
	env[wakeArbitrationEnv] = "0"
	if got, err := configFromEnv(lookup(env)).wakeArbitration(); err != nil || got != 0 {
		t.Errorf("0 = %v, %v; want arbitration off", got, err)
	}
	env[wakeArbitrationEnv] = "350"
	if got, err := configFromEnv(lookup(env)).wakeArbitration(); err != nil || got != 350*time.Millisecond {
		t.Errorf("350 = %v, %v; want 350ms", got, err)
	}
	for _, bad := range []string{"a fifth of a second", "-5", "0.2s"} {
		env[wakeArbitrationEnv] = bad
		err := configFromEnv(lookup(env)).validate()
		if err == nil || !strings.Contains(err.Error(), wakeArbitrationEnv) {
			t.Errorf("%q: err = %v; want it to name %s", bad, err, wakeArbitrationEnv)
		}
	}
}
