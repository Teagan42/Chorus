package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/session"
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

// Resuming opens a second session on a conversation the first one is still
// writing to, so one log has two writers. The store rejects a sequence number
// that is not exactly one past the last, so an unordered pair fails a turn.
// The ordering belongs to the journal, which is the only thing both sessions
// share (internal/journal/journal.go).
//
// verifies SPEC §4.5, §8
func TestAMigratedWakeSharesTheLogItResumed(t *testing.T) {
	r := newRig(t, []step{{act: session.TurnEnd{FinishReason: "stop", Completion: `{}`}}}, nil)

	kitchen := r.open(t, "alice")
	office, err := r.sup.Open(context.Background(), session.Wake{Satellite: "office", PersonID: "alice"})
	if err != nil {
		t.Fatalf("migrated wake: %v", err)
	}
	if !office.Resumed() || office.ConversationID() != kitchen.ConversationID() {
		t.Fatalf("second wake opened %q resumed=%v, want %q resumed=true",
			office.ConversationID(), office.Resumed(), kitchen.ConversationID())
	}

	first, second := heard(kitchen, "turn it down"), heard(office, "and the lights")
	wait(t, first)
	wait(t, second)

	events, err := r.store.Events(context.Background(), kitchen.ConversationID())
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
	// Both wakes and both turns, in one log.
	if len(events) < 6 {
		t.Errorf("got %d events, want both sessions' wakes and turns: %v", len(events), r.kinds(t, kitchen.ConversationID()))
	}
}
