package otel

import (
	"fmt"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Every kind the taxonomy declares is a decision about what a trace shows,
// even when the decision is nothing: a new kind fails here until it is made.
//
// verifies SPEC §8
func TestEveryEventKindIsDecided(t *testing.T) {
	for _, k := range journal.AllKinds {
		if _, ok := handlers[k]; !ok {
			t.Errorf("%s: no decision on what a trace shows of it", k)
		}
	}
	if len(handlers) != len(journal.AllKinds) {
		t.Errorf("%d kinds decided, %d declared", len(handlers), len(journal.AllKinds))
	}
}

// A live builder holds a bounded number of conversations. One crowded out,
// a session the daemon never closed, is ended where it was last heard and
// marked unfinished rather than held forever.
//
// verifies SPEC §8
func TestAForgottenConversationEndsUnfinished(t *testing.T) {
	b := NewBuilder(Options{})
	at := time.Date(2025, time.October, 9, 6, 0, 0, 0, time.UTC)
	open := func(i int) []Span {
		return b.Add(journal.Event{
			ConversationID: fmt.Sprintf("conv-%04d-kitchen", i), Seq: 1, At: at.Add(time.Duration(i) * time.Second),
			Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan"},
		})
	}
	for i := range maxConversations {
		if out := open(i); len(out) != 0 {
			t.Fatalf("conversation %d crowded out %d spans with room to spare", i, len(out))
		}
	}
	out := open(maxConversations)
	if len(out) != 1 || out[0].Trace != traceOf("conv-0000-kitchen") {
		t.Fatalf("crowding out gave %+v, want the oldest conversation's session", out)
	}
	if v, _ := out[0].Attr("chorus.unfinished"); v != true || !out[0].End.Equal(at) {
		t.Errorf("crowded-out session = %+v, want unfinished at its last event", out[0])
	}
	if got := len(b.Unfinished()); got != maxConversations {
		t.Errorf("%d sessions left open, want %d", got, maxConversations)
	}
}
