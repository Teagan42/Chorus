package session_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// ovenDone is the oven timer Alan set going off in the kitchen.
var ovenDone = session.Announcement{
	Text: "The oven timer is done.", Source: session.SourceTimer, TimerID: "t_0a7e11c3",
	RequestedBy: "alan", FromSatellite: "kitchen", FromConversation: "conv-1840-kitchen",
}

// wine is Teagan asking the kitchen, from the office, whether anyone wants
// wine, and listening for the answer.
var wine = session.Announcement{
	Text: "Dinner is ready. Does anyone want wine?", Source: session.SourceRequest,
	RequestedBy: "teagan", FromSatellite: "office", FromConversation: "conv-1838-office",
	StartConversation: true,
}

func awaitDone(t *testing.T, s *session.Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("the session never closed")
	}
}

// The oven goes off with nobody talking to the kitchen. A session opens with
// no wake word, says it, and closes as announced: proactive speech is a
// session with no wake word, and nobody was asked anything (SPEC §4).
//
// verifies SPEC §4, §8
func TestAnAnnouncementIsASessionWithNoWakeWord(t *testing.T) {
	r := newRig(t, nil, nil)
	s, err := r.sup.Announce(context.Background(), "kitchen", ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)

	want := []journal.Kind{
		journal.KindSessionOpened, journal.KindAnnouncementMade, journal.KindToolCalled,
		journal.KindSpeechSpoken, journal.KindToolResult, journal.KindSessionClosed,
	}
	if got := r.kinds(t, s.ConversationID()); !slices.Equal(got, want) {
		t.Fatalf("log = %v, want %v", got, want)
	}
	opened := r.eventOf(t, s.ConversationID(), journal.KindSessionOpened)
	if opened.Fields["announced"] != "true" || opened.Fields["speaker_id"] != "" || opened.Fields["satellite"] != "kitchen" {
		t.Errorf("session_opened = %v", opened.Fields)
	}
	made := r.eventOf(t, s.ConversationID(), journal.KindAnnouncementMade)
	if made.Fields["source"] != "timer" || made.Fields["timer_id"] != "t_0a7e11c3" || made.Fields["requested_by"] != "alan" {
		t.Errorf("announcement_made = %v", made.Fields)
	}
	if _, asked := made.Fields["start_conversation"]; asked && made.Fields["start_conversation"] != "false" {
		t.Errorf("a timer asked for an answer: %v", made.Fields)
	}
	if closed := r.eventOf(t, s.ConversationID(), journal.KindSessionClosed); closed.Fields["reason"] != "announced" {
		t.Errorf("closed as %q, want announced", closed.Fields["reason"])
	}
	st := r.state(t, s.ConversationID())
	if !slices.Equal(st.Spoken, []string{"The oven timer is done."}) || len(st.Dialogue) != 1 || st.Dialogue[0].Announces == nil {
		t.Errorf("spoken = %q, dialogue = %+v", st.Spoken, st.Dialogue)
	}
	if got := r.engine.asks(); len(got) != 0 {
		t.Errorf("the model was asked %d times for a timer going off", len(got))
	}
	if s.Answerable() {
		t.Error("a timer going off may be answered with no wake word")
	}
}

// Teagan's question for the kitchen asks for an answer, so the session stays
// open after saying it, and Alice answers with no wake word. The model is
// asked with the question it said, and why it said it, before her answer.
//
// verifies SPEC §4, §4.5
func TestAnAnnouncementThatAsksIsAnsweredInItsOwnConversation(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "call_a2", Tool: "announce", Args: `{"text":"Alice would like a glass of red.","room":"office"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	relayed := make(chan string, 1)
	r := newRig(t, steps, map[string]session.Tool{"announce": session.ToolFunc(func(_ context.Context, args string) (string, error) {
		relayed <- args
		return `{"announced_in":["office"]}`, nil
	})})
	s, err := r.sup.Announce(context.Background(), "kitchen", wine)
	if err != nil {
		t.Fatal(err)
	}
	r.awaitKind(t, s.ConversationID(), journal.KindSpeechSpoken)
	if !s.Answerable() {
		t.Fatal("a question for the kitchen cannot be answered there")
	}

	errc := make(chan error, 1)
	go func() {
		errc <- s.Heard(context.Background(), session.Transcript{Text: "yes a glass of red please", SpeakerID: "alice", AudioRef: "blob://mic/wine"})
	}()
	wait(t, errc)
	if got := <-relayed; got != `{"text":"Alice would like a glass of red.","room":"office"}` {
		t.Errorf("relayed %s", got)
	}

	select {
	case <-s.Done():
		t.Fatal("the session closed under the answer")
	default:
	}
	asks := r.engine.asks()
	if len(asks) == 0 {
		t.Fatal("the answer was never asked about")
	}
	d := asks[0].Dialogue
	if len(d) != 2 || d[0].Kind != journal.EntrySaid || d[0].Text != wine.Text || d[1].Text != "yes a glass of red please" {
		t.Fatalf("dialogue = %+v, want the question then the answer", d)
	}
	if a := d[0].Announces; a == nil || a.RequestedBy != "teagan" || a.FromSatellite != "office" || !a.StartConversation {
		t.Errorf("the model was not told whose question it asked: %+v", d[0].Announces)
	}
	if asks[0].Speaker != "alice" {
		t.Errorf("answered for %q", asks[0].Speaker)
	}
}

// The oven goes off just after Alice cut the kitchen off mid-answer. Her
// barge-in cut the turn, and the announcement is not part of that turn:
// it plays, queued in her conversation, so the next ask knows the timer
// went off.
//
// verifies SPEC §4.2, §4.4
func TestAnAnnouncementJoinsTheConversationInTheRoom(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")
	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)
	if ok, err := s.BargeIn(context.Background(), interruption(420)); err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)
	close(r.speaker.release)

	if err := s.Announce(context.Background(), ovenDone); err != nil {
		t.Fatal(err)
	}
	if got := r.speaker.wrote(t); got != "The oven timer is done." {
		t.Errorf("played %q", got)
	}
	r.awaitKind(t, s.ConversationID(), journal.KindSpeechSpoken)

	st := r.state(t, s.ConversationID())
	last := st.Dialogue[len(st.Dialogue)-1]
	if last.Kind != journal.EntrySaid || last.Text != "The oven timer is done." || last.Announces == nil || last.Announces.Source != "timer" {
		t.Errorf("last dialogue entry = %+v, want the oven, as an announcement", last)
	}
	if !st.Open || st.Announced {
		t.Errorf("open = %v, announced = %v: Alice's conversation is hers, and still open", st.Open, st.Announced)
	}
}

// The oven goes off while the kitchen is still answering Alice, so it queues
// behind the answer. She cuts the answer off: the turn is dropped, the timer
// is not, and it plays once the cut has. Its Heard is told so.
//
// verifies SPEC §4.2, §4.4
func TestAnAnnouncementQueuedBeforeABargeInStillPlays(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")
	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)

	oven, said := ovenDone, make(chan bool, 1)
	oven.Heard = said
	if err := s.Announce(context.Background(), oven); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.BargeIn(context.Background(), interruption(420)); err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	if got := r.speaker.wrote(t); got != "The oven timer is done." {
		t.Fatalf("played %q after the cut, want the oven", got)
	}
	close(r.speaker.release)
	wait(t, errc)
	select {
	case ok := <-said:
		if !ok {
			t.Error("the oven was played, and Heard was told it was not")
		}
	case <-time.After(patience):
		t.Fatal("Heard was never told")
	}
	st := r.state(t, s.ConversationID())
	if last := st.Dialogue[len(st.Dialogue)-1]; last.Text != "The oven timer is done." || last.Announces == nil {
		t.Errorf("last dialogue entry = %+v, want the oven", last)
	}
}

// Kokoro is down when the oven goes off. The announcement is queued and
// logged, but nothing plays, and Heard is told so: queued is not heard.
//
// verifies SPEC §7
func TestAnAnnouncementTheSpeakerCannotPlayIsNotHeard(t *testing.T) {
	r := newRig(t, nil, nil)
	r.speaker.broken = true
	oven, said := ovenDone, make(chan bool, 1)
	oven.Heard = said
	s, err := r.sup.Announce(context.Background(), "kitchen", oven)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-said:
		if ok {
			t.Error("Heard was told a timer nobody could hear was heard")
		}
	case <-time.After(patience):
		t.Fatal("Heard was never told")
	}
	awaitDone(t, s)
}

// An announcement is not the answer to an ask, so its first frame is not the
// turn's: the household's wait for an answer is not measured off a timer.
//
// verifies SPEC §11
func TestAnAnnouncementsFirstFrameIsNotATurnsFirstAudio(t *testing.T) {
	r := newDACRig(t, nil, &dacSpeaker{lags: []time.Duration{180 * time.Millisecond}})
	s, err := r.sup.Announce(context.Background(), "kitchen", ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	awaitDone(t, s)
	if got := starts(r.events(t, s.ConversationID())); len(got) != 0 {
		t.Errorf("speech_started = %v for an announcement", got)
	}
}

// A session that has closed takes no announcement: the listener opens a new
// one instead. Nothing to say is refused before anything is logged.
func TestAnAnnouncementIsRefusedByAClosedSession(t *testing.T) {
	r := newRig(t, nil, nil)
	s := r.open(t, "alice")
	if err := s.Announce(context.Background(), session.Announcement{Text: "  ", Source: session.SourceTimer}); err == nil {
		t.Error("announced nothing")
	}
	if err := s.Close(context.Background(), "model_ended"); err != nil {
		t.Fatal(err)
	}
	if err := s.Announce(context.Background(), ovenDone); !errors.Is(err, session.ErrClosed) {
		t.Errorf("announce on a closed session: %v, want ErrClosed", err)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindAnnouncementMade); n != 0 {
		t.Errorf("%d announcements logged by a closed session", n)
	}
}
