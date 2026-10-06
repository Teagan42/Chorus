// Package session is the actor/supervisor runtime (SPEC §4). A session owns
// concurrent children; there is no pipeline and no stage enum.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// MigrationWindow is how long the same person may wake a different satellite
// and still land in the same logical conversation (SPEC §4.5).
const MigrationWindow = 2 * time.Minute

// Conversations keys conversations on the person. The audio stream keys on the
// device, which is what makes device migration fall out for free (SPEC §4.5).
type Conversations struct {
	clock  journal.Clock
	window time.Duration

	mu    sync.Mutex
	last  map[string]time.Time
	byKey map[string]string
}

// NewConversations binds the person index to a clock and a migration window.
func NewConversations(clock journal.Clock, window time.Duration) *Conversations {
	return &Conversations{
		clock:  clock,
		window: window,
		last:   map[string]time.Time{},
		byKey:  map[string]string{},
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
