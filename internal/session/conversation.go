// Package session is the actor/supervisor runtime (SPEC §4). A session owns
// concurrent children; there is no pipeline and no stage enum.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// MigrationWindow is how long the same person may wake a different satellite
// and still land in the same logical conversation (SPEC §4.5).
const MigrationWindow = 2 * time.Minute

// owner is the live session holding a conversation. Narrow on purpose: the
// person index must not depend on the session's internals.
type owner interface {
	yield() error
}

// Conversations keys conversations on the person. The audio stream keys on the
// device, which is what makes device migration fall out for free (SPEC §4.5).
//
// It also holds the one live session per conversation, because that is the only
// state a per-satellite supervisor shares with its peers.
type Conversations struct {
	clock  journal.Clock
	window time.Duration

	mu    sync.Mutex
	last  map[string]time.Time
	byKey map[string]string
	live  map[string]owner
	seats map[string]*sync.Mutex
}

// NewConversations binds the person index to a clock and a migration window.
func NewConversations(clock journal.Clock, window time.Duration) *Conversations {
	return &Conversations{
		clock:  clock,
		window: window,
		last:   map[string]time.Time{},
		byKey:  map[string]string{},
		live:   map[string]owner{},
		seats:  map[string]*sync.Mutex{},
	}
}

// Open returns the person's conversation, resuming one inside the migration
// window. An unidentified speaker always gets a fresh conversation: a guest
// must not inherit someone else's context (SPEC §5).
func (c *Conversations) Open(personID string) (string, bool) {
	now := c.clock.Now()
	if personID == "" {
		return newID(), false
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if id, ok := c.byKey[personID]; ok && now.Sub(c.last[personID]) <= c.window {
		c.last[personID] = now
		return id, true
	}
	id := newID()
	c.byKey[personID] = id
	c.last[personID] = now
	return id, false
}

// seat serialises one person's handoffs for the whole of Open. Two satellites
// in earshot of one wake word is ordinary, and without this the session that
// loses the race can record its open after its own close.
//
// Keyed on the person, not the conversation: conversation ids are never reused,
// so that table would grow for the life of the process. A guest gets an
// unshared lock because a guest never resumes and so displaces nobody.
func (c *Conversations) seat(personID string) *sync.Mutex {
	if personID == "" {
		return &sync.Mutex{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	m := c.seats[personID]
	if m == nil {
		m = &sync.Mutex{}
		c.seats[personID] = m
	}
	return m
}

// claim installs s as the conversation's live session and returns the one it
// displaced. The caller yields that one: doing it here would hold the index
// across a journal write and a TTS teardown.
func (c *Conversations) claim(conversationID string, s owner) owner {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev := c.live[conversationID]
	c.live[conversationID] = s
	return prev
}

// release drops s only while it is still the live session, so a session that
// has already been displaced cannot evict the one that displaced it.
func (c *Conversations) release(conversationID string, s owner) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.live[conversationID] == s {
		delete(c.live, conversationID)
	}
}

// Touch records activity so an open conversation does not expire mid-session.
func (c *Conversations) Touch(personID string) {
	if personID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.byKey[personID]; ok {
		c.last[personID] = c.clock.Now()
	}
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
