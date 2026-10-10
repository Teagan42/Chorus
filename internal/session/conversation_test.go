package session_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// verifies SPEC §4.5
func TestConversationResumesForSamePersonOnAnotherDevice(t *testing.T) {
	clk := newClock()
	c := session.NewConversations(clk, 2*time.Minute)

	first, resumed := c.Open("alice")
	if resumed {
		t.Fatalf("first wake resumed an existing conversation")
	}
	if first == "" {
		t.Fatal("empty conversation id")
	}

	// The audio stream moved to another satellite; the conversation did not.
	clk.advance(90 * time.Second)
	second, resumed := c.Open("alice")
	if !resumed || second != first {
		t.Errorf("migration gave %q resumed=%v, want %q resumed=true", second, resumed, first)
	}
}

// verifies SPEC §4.5
func TestConversationExpiresAfterMigrationWindow(t *testing.T) {
	clk := newClock()
	c := session.NewConversations(clk, 2*time.Minute)

	first, _ := c.Open("alice")
	clk.advance(2*time.Minute + time.Nanosecond)

	second, resumed := c.Open("alice")
	if resumed || second == first {
		t.Errorf("stale wake gave %q resumed=%v, want a fresh conversation", second, resumed)
	}
}

// verifies SPEC §4.5
func TestConversationIsPerPerson(t *testing.T) {
	clk := newClock()
	c := session.NewConversations(clk, 2*time.Minute)

	alice, _ := c.Open("alice")
	bob, _ := c.Open("bob")
	if alice == bob {
		t.Errorf("two people share conversation %q", alice)
	}

	// An unidentified speaker must never inherit someone else's conversation.
	guest1, _ := c.Open("")
	guest2, resumed := c.Open("")
	if guest1 == guest2 || resumed {
		t.Errorf("guest wakes shared %q resumed=%v", guest1, resumed)
	}
}

// migrate wakes a second satellite for the same person, which resumes their
// conversation (SPEC §4.5).
func migrate(t *testing.T, r *rig, satellite, person string) *session.Session {
	t.Helper()
	s, err := r.sup.Open(context.Background(), session.Wake{Satellite: satellite, PersonID: person})
	if err != nil {
		t.Fatalf("migrated wake: %v", err)
	}
	if !s.Resumed() {
		t.Fatalf("wake on %s started a new conversation %q", satellite, s.ConversationID())
	}
	return s
}

// A conversation has one live session. The person has one pair of ears, so the
// session left behind on the old satellite hands over rather than running on
// beside the new one (SPEC §4.5).
//
// verifies SPEC §4.5
func TestAMigratedWakeDisplacesTheSessionItResumed(t *testing.T) {
	r := newRig(t, nil, nil)

	kitchen := r.open(t, "alice")
	office := migrate(t, r, "office", "alice")
	if office.ConversationID() != kitchen.ConversationID() {
		t.Fatalf("resumed wake opened %q, want %q", office.ConversationID(), kitchen.ConversationID())
	}

	select {
	case <-kitchen.Done():
	case <-time.After(patience):
		t.Fatal("the vacated session is still live")
	}
	select {
	case <-office.Done():
		t.Fatal("the session that took over closed with it")
	default:
	}

	// The close has to precede the open: a reducer folding them the other way
	// round ends on the session that lost the conversation.
	if got := r.kinds(t, kitchen.ConversationID()); !slices.Equal(got, []journal.Kind{
		journal.KindSessionOpened, journal.KindSessionClosed, journal.KindSessionOpened,
	}) {
		t.Fatalf("log = %v, want open, close, open", got)
	}
	st := r.state(t, kitchen.ConversationID())
	if !st.Open || st.Satellite != "office" {
		t.Errorf("state = open %v on %q, want open on office", st.Open, st.Satellite)
	}
}

// The handoff is recorded as a close of the session and a resumed open, not as
// the end of the conversation, so the log says which device the person left and
// which one they are on (SPEC §15 item 4).
//
// verifies SPEC §4.5, §8
func TestTheHandoffRecordsWhichDeviceThePersonLeft(t *testing.T) {
	r := newRig(t, nil, nil)

	kitchen := r.open(t, "alice")
	migrate(t, r, "office", "alice")

	events, err := r.store.Events(context.Background(), kitchen.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	want := []map[string]string{
		{"satellite": "kitchen", "resumed": "false"},
		{"satellite": "kitchen", "reason": "migrated"},
		{"satellite": "office", "resumed": "true"},
	}
	if len(events) != len(want) {
		t.Fatalf("log = %v, want open, close, open", r.kinds(t, kitchen.ConversationID()))
	}
	for i, want := range want {
		for field, value := range want {
			if got := events[i].Fields[field]; got != value {
				t.Errorf("event %d (%s): %s = %q, want %q", i, events[i].Kind, field, got, value)
			}
		}
	}
}

// The satellite the person walked away from must not time out the conversation
// they are still using. Each session runs its own silence backstop off its own
// activity channel, so a session left behind closes a live conversation and
// journal.Reduce folds that into Open=false while later events keep arriving.
//
// verifies SPEC §4.5, §8
func TestOnlyTheSatelliteThePersonIsOnCanTimeOutTheConversation(t *testing.T) {
	r := newRig(t, nil, nil)

	kitchen := r.open(t, "alice")
	r.clock.awaitTimers(t, 1)
	office := migrate(t, r, "office", "alice")
	r.clock.awaitTimers(t, 2)

	// Both backstops are armed at the same deadline, so this fires whichever
	// ones are still being waited on.
	r.clock.advance(session.DefaultSilence)
	for _, s := range []*session.Session{kitchen, office} {
		select {
		case <-s.Done():
		case <-time.After(patience):
			t.Fatal("a session outlived its silence backstop")
		}
	}

	var timedOut []string
	events, err := r.store.Events(context.Background(), kitchen.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		if e.Kind == journal.KindSessionClosed && e.Fields["reason"] == "silence_timeout" {
			timedOut = append(timedOut, e.Fields["satellite"])
		}
	}
	if !slices.Equal(timedOut, []string{"office"}) {
		t.Errorf("silence closed %v, want only the satellite the person is on", timedOut)
	}
}

// A displaced session stops owning the conversation but its detached children
// do not stop writing to it, so one log still has two sessions' writers. The
// store rejects a sequence number that is not exactly one past the last, so an
// unordered pair loses an event (internal/journal/journal.go).
//
// verifies SPEC §4.5, §8
func TestADisplacedSessionsChildrenKeepTheLogGapless(t *testing.T) {
	// media_search declares on_interrupt: detach (ADR-0011), so the call
	// outlives the session that made it.
	r := newRig(t, []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}, nil)
	gate := newGateTool(`{"hits":3}`)
	r.tools["media_search"] = gate

	kitchen := r.open(t, "alice")
	errc := heard(kitchen, "find zeppelin")
	gate.enter(t)

	office := migrate(t, r, "office", "alice")
	// The displaced turn unwinds with the session; its detached child does not.
	wait(t, errc)

	// The detached child lands its result while the new session is mid-turn.
	second := heard(office, "the other one")
	close(gate.release)
	r.awaitCall(t, office.ConversationID(), "c1")
	wait(t, second)

	events, err := r.store.Events(context.Background(), office.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	// Gapless and unique: replay reduces in sequence order, so a repeated or
	// skipped number is a log it cannot read back.
	for i, e := range events {
		if want := uint64(i + 1); e.Seq != want {
			t.Fatalf("event %d (%s): seq = %d, want %d", i, e.Kind, e.Seq, want)
		}
	}
}

// A busy kitchen at breakfast: Teagan asks for the coffee, Alan for the porch
// light and Alice for the weather, all before the first is answered. The
// listener hands a session one utterance at a time, but Session keeps its
// state under a lock so that nothing depends on that, and these tests hear
// from their own goroutines. Attribution flips while another call is reading
// who owns the conversation; every one of them is heard, and the race detector
// sees no unguarded read of the speaker. The window is narrow, so the morning
// repeats until a regression would show.
//
// verifies SPEC §5
func TestPeopleTalkingOverEachOtherAreAllHeard(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	asks := []struct{ who, text string }{
		{"teagan", "start the coffee maker"},
		{"alan", "is the porch light on"},
		{"alice", "what's the weather today"},
	}
	for range 100 {
		r := newRig(t, steps, nil)
		s := r.open(t, "teagan")

		var turns []<-chan error
		for _, a := range asks {
			turns = append(turns, heardFrom(s, a.who, a.text))
		}
		for _, turn := range turns {
			wait(t, turn)
		}

		var who []string
		for _, e := range r.events(t, s.ConversationID()) {
			if e.Kind == journal.KindUtteranceTranscribed {
				who = append(who, e.Fields["speaker_id"])
			}
		}
		slices.Sort(who)
		if !slices.Equal(who, []string{"alan", "alice", "teagan"}) {
			t.Fatalf("utterances heard from %v, want alan, alice and teagan", who)
		}
	}
}

// Alice chimes into Teagan's conversation in the kitchen, and attribution
// flips to her. Her words keep Teagan's conversation alive, the one in use,
// not the older one Alice left in the study: Teagan resumes from the office,
// and Alice back in the study starts afresh.
//
// verifies SPEC §4.5, §5
func TestAFlippedSpeakerKeepsTheConversationInUseAlive(t *testing.T) {
	r := newRigWith(t, nil, nil, nil, func(c *session.Config) {
		// Long enough that no backstop closes anything while the clock moves.
		c.Silence = 10 * time.Minute
	})
	study, err := r.sup.Open(context.Background(), session.Wake{Satellite: "study", PersonID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	r.clock.advance(time.Minute)
	kitchen := r.open(t, "teagan")
	r.clock.advance(90 * time.Second)
	wait(t, heardFrom(kitchen, "alice", "is it going to rain this afternoon"))
	r.clock.advance(90 * time.Second)

	office := migrate(t, r, "office", "teagan")
	if office.ConversationID() != kitchen.ConversationID() {
		t.Errorf("office resumed %q, want the kitchen's %q", office.ConversationID(), kitchen.ConversationID())
	}
	back, err := r.sup.Open(context.Background(), session.Wake{Satellite: "study", PersonID: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if back.Resumed() || back.ConversationID() == study.ConversationID() {
		t.Errorf("alice resumed %q, quiet for four minutes; want a fresh conversation", back.ConversationID())
	}
}

// Teagan walks from the kitchen to the office mid-forecast. The kitchen's
// cut is recorded where its device says it stopped, and that takes the
// device a moment: the close waits for it, so the log reads the cut, then
// the kitchen's close, then the office's open, and never a cut that lands
// in the office's session.
//
// verifies SPEC §4.4, §4.5, §8
func TestAMigrationRecordsTheCutBeforeTheClose(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "Tomorrow will be sunny with a high of", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("Tomorrow will be sunny")
	r.speaker.settle, r.speaker.settling = make(chan struct{}), make(chan struct{}, 1)

	kitchen := r.open(t, "teagan")
	errc := heard(kitchen, "what's the weather tomorrow")
	r.speaker.wrote(t)

	opened := make(chan *session.Session, 1)
	go func() {
		s, err := r.sup.Open(context.Background(), session.Wake{Satellite: "office", PersonID: "teagan"})
		if err != nil {
			t.Errorf("migrated wake: %v", err)
		}
		opened <- s
	}()
	select {
	case <-r.speaker.settling:
	case <-time.After(patience):
		t.Fatal("the kitchen's speech was never cut")
	}
	close(r.speaker.settle)
	select {
	case <-opened:
	case <-time.After(patience):
		t.Fatal("the office never opened")
	}
	wait(t, errc)

	var order []string
	for _, e := range r.events(t, kitchen.ConversationID()) {
		switch e.Kind {
		case journal.KindSpeechTruncated, journal.KindSessionClosed:
			order = append(order, string(e.Kind))
		case journal.KindSessionOpened:
			order = append(order, string(e.Kind)+" "+e.Fields["satellite"])
		}
	}
	want := []string{"session_opened kitchen", "speech_truncated", "session_closed", "session_opened office"}
	if !slices.Equal(order, want) {
		t.Errorf("log = %q, want %q", order, want)
	}
}
