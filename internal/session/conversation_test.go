package session_test

import (
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
