package journal

import (
	"strconv"
	"testing"
	"time"
)

// The table must not outlive its writers. Conversation ids are never reused, so
// an entry left behind is a mutex held for as long as the process runs.
func TestTheLockTableDropsAConversationWithNoWriters(t *testing.T) {
	var l seqLocks

	for i := range 64 {
		release := l.lock(strconv.Itoa(i))
		if got := len(l.held); got != 1 {
			t.Fatalf("conversation %d: %d entries held, want 1", i, got)
		}
		release()
	}
	if got := len(l.held); got != 0 {
		t.Errorf("%d entries left behind", got)
	}
}

// A queued writer has to keep the entry alive, or it would wake holding a lock
// the table no longer names and the writer after it would get a fresh one.
func TestAQueuedWriterWaitsAndStillCleansUp(t *testing.T) {
	var l seqLocks

	first := l.lock("conv-1")
	second := make(chan func(), 1)
	go func() { second <- l.lock("conv-1") }()

	select {
	case <-second:
		t.Fatal("two writers held one conversation's lock at once")
	case <-time.After(50 * time.Millisecond):
	}

	first()
	(<-second)()
	if got := len(l.held); got != 0 {
		t.Errorf("%d entries left behind after a contended handoff", got)
	}
}
