package listen_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

var ovenDone = session.Announcement{
	Text: "The oven timer is done.", Source: session.SourceTimer, TimerID: "t_0a7e11c3",
	RequestedBy: "alan", FromSatellite: "kitchen", FromConversation: "conv-1840-kitchen",
}

var wine = session.Announcement{
	Text: "Dinner is ready. Does anyone want wine?", Source: session.SourceRequest,
	RequestedBy: "teagan", FromSatellite: "office", FromConversation: "conv-1838-office",
	StartConversation: true,
}

// The oven goes off in an empty kitchen. The announcement opens its own
// session, but the mic is not its to hear: a wake word said over it is not
// taken, and once it has been said the kitchen wakes as it always does.
//
// verifies SPEC §4, §4.5
func TestAnAnnouncementNobodyIsAskedToAnswerLeavesTheMicAlone(t *testing.T) {
	r := newRig(t, silent())
	r.speaker.hold = true

	conv, err := r.l.Announce(context.Background(), ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.speaker.wrote(t); got != ovenDone.Text {
		t.Errorf("played %q", got)
	}
	if r.l.Session() != nil {
		t.Error("the mic feeds a timer going off")
	}

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("thanks", alan), 4*chunkBytes)
	r.settled(t)
	r.speaker.let()
	closed := r.awaitKind(t, conv, journal.KindSessionClosed, 1)
	if closed.Fields["reason"] != "announced" {
		t.Errorf("closed as %q, want announced", closed.Fields["reason"])
	}
	if n := r.count(t, conv, journal.KindUtteranceTranscribed); n != 0 {
		t.Errorf("%d utterances heard by a timer going off", n)
	}

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("add two minutes to the oven", alan), 4*chunkBytes)
	s := r.session(t)
	if s.ConversationID() == conv {
		t.Error("the wake joined the timer's conversation")
	}
	first := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)
	if first.Fields["text"] != "add two minutes to the oven" {
		t.Errorf("the session opened on %q: the wake said over the timer was taken", first.Fields["text"])
	}
}

// Teagan's question for the kitchen asks for an answer. The mic feeds its
// session as a woken one's would, so Alan answers with no wake word, in the
// conversation the question was asked in.
//
// verifies SPEC §4, §4.5
func TestAnAnnouncementThatAsksIsHeardWithNoWakeWord(t *testing.T) {
	r := newRig(t, silent())
	conv, err := r.l.Announce(context.Background(), wine)
	if err != nil {
		t.Fatal(err)
	}
	r.awaitKind(t, conv, journal.KindSpeechSpoken, 1)
	if s := r.l.Session(); s == nil || s.ConversationID() != conv {
		t.Fatalf("the mic feeds %v, want the question's conversation", s)
	}

	r.utter(t, r.line("yes a glass of red please", alan), 4*chunkBytes)
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	if heard.Fields["speaker_id"] != "alan" || heard.Fields["text"] != "yes a glass of red please" {
		t.Errorf("heard %v", heard.Fields)
	}
	await(t, "the turn", func() bool { return len(r.engine.heard()) == 1 })
	if in := r.engine.heard()[0]; in.Speaker != "alan" || len(in.Dialogue) != 2 || in.Dialogue[0].Announces == nil {
		t.Errorf("the model was asked %+v", in)
	}
}

// Alan is talking to the kitchen when the oven goes off. The timer is said
// in his session, behind what it is saying, not over it on a second one.
//
// verifies SPEC §4.2
func TestAnAnnouncementJoinsTheSessionOpenOnTheSatellite(t *testing.T) {
	r := newRig(t, silent())
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("what can I make with leeks", alan), 4*chunkBytes)
	s := r.session(t)
	r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)

	conv, err := r.l.Announce(context.Background(), ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	if conv != s.ConversationID() {
		t.Errorf("announced in %s, want Alan's %s", conv, s.ConversationID())
	}
	r.awaitKind(t, conv, journal.KindSpeechSpoken, 1)
	if n := r.count(t, conv, journal.KindSessionOpened); n != 1 {
		t.Errorf("%d sessions opened in Alan's conversation", n)
	}
}

// Two timers going off at once on a quiet kitchen share one session: one
// speaker, so one session speaking on it.
//
// verifies SPEC §4.2
func TestTwoAnnouncementsAtOnceShareASession(t *testing.T) {
	r := newRig(t, silent())
	r.speaker.hold = true
	first, err := r.l.Announce(context.Background(), ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	r.speaker.wrote(t)
	rice := ovenDone
	rice.Text, rice.TimerID = "The rice timer is done.", "t_7f3a9b21"
	second, err := r.l.Announce(context.Background(), rice)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("announced in %s and %s, want one session", first, second)
	}
	r.speaker.let()
	r.awaitKind(t, first, journal.KindSessionClosed, 1)
	if n := r.count(t, first, journal.KindSpeechSpoken); n != 2 {
		t.Errorf("%d said before the close, want both", n)
	}
}

// A link that has gone says nothing. The daemon reads this as a satellite
// that is not connected.
func TestAnAnnouncementOnALinkThatHasGoneIsRefused(t *testing.T) {
	r := newRig(t, silent())
	r.cancel()
	select {
	case <-r.l.Done():
	case <-time.After(patience):
		t.Fatal("the listener never finished")
	}
	if _, err := r.l.Announce(context.Background(), ovenDone); !errors.Is(err, listen.ErrLinkClosed) {
		t.Errorf("announce on a gone link: %v", err)
	}
}
