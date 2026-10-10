package session_test

import (
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// The satellite contributes location as turn metadata, not identity: each
// turn is told the room it was heard in, from the log, so Alice's "and the
// lights" after walking from the kitchen to the study means the study's.
//
// verifies SPEC §5
func TestEachTurnIsToldTheRoomItWasHeardIn(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "Done.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) {
		c.Rooms = map[string]string{"kitchen": "kitchen", "office": "study"}
	})

	kitchen, err := r.sup.Open(t.Context(), session.Wake{Satellite: "kitchen", PersonID: "alice"})
	if err != nil {
		t.Fatalf("open kitchen: %v", err)
	}
	wait(t, heard(kitchen, "turn off the lights"))
	if err := kitchen.Close(t.Context(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.clock.advance(40 * time.Second)
	office, err := r.sup.Open(t.Context(), session.Wake{Satellite: "office", PersonID: "alice"})
	if err != nil {
		t.Fatalf("open office: %v", err)
	}
	wait(t, heard(office, "and the lights in here"))

	asks := r.engine.asks()
	if len(asks) != 2 {
		t.Fatalf("asked %d times, want once per utterance", len(asks))
	}
	if asks[0].Room != "kitchen" || asks[1].Room != "study" {
		t.Errorf("turns were told rooms %q then %q, want kitchen then study", asks[0].Room, asks[1].Room)
	}
	// From the log: a replay is told the same room the turn was.
	if got := r.state(t, office.ConversationID()).Room; got != "study" {
		t.Errorf("replayed room = %q, want study", got)
	}
}

// A satellite the inventory gives no room records none, and its turns are
// told none: the model is not handed an empty location to read aloud.
//
// verifies SPEC §5
func TestASatelliteInNoRoomTellsTheTurnNothing(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) {
		c.Rooms = map[string]string{"office": "study"}
	})

	s := r.open(t, "alan")
	wait(t, heard(s, "what time is it"))

	if _, ok := r.eventOf(t, s.ConversationID(), journal.KindSessionOpened).Fields["room"]; ok {
		t.Error("session_opened recorded a room for a satellite the inventory puts in none")
	}
	if asks := r.engine.asks(); len(asks) != 1 || asks[0].Room != "" {
		t.Errorf("asks = %+v, want one told no room", asks)
	}
}

// An announcement opens its session on the satellite it is said at, so the
// oven timer's conversation knows it went off in the kitchen.
//
// verifies SPEC §5
func TestAnAnnouncedSessionRecordsItsRoom(t *testing.T) {
	r := newRigWith(t, nil, nil, nil, func(c *session.Config) {
		c.Rooms = map[string]string{"kitchen": "kitchen"}
	})

	s, err := r.sup.Announce(t.Context(), "kitchen", session.Announcement{Text: "The oven timer is done.", Source: "timer"})
	if err != nil {
		t.Fatalf("announce: %v", err)
	}
	if got := r.eventOf(t, s.ConversationID(), journal.KindSessionOpened).Fields["room"]; got != "kitchen" {
		t.Errorf("room = %q, want kitchen", got)
	}
}
