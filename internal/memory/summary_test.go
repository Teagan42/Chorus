package memory_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/memory"
	"github.com/teaganglenn/chorus/internal/session"
)

// Teagan and Alan talked about the garage door together, with a visitor
// chiming in. The summary is kept for both of them, and for nobody else.
//
// verifies SPEC §5
func TestASummaryIsKeptForEveryoneWhoWasThere(t *testing.T) {
	store := memory.NewMemStore()
	r := memory.Recaller(store, memory.RecallConfig{})
	garage := journal.Summary{
		ConversationID: "conv-garage-0812",
		At:             friday.Add(-14 * time.Hour),
		Text:           "Teagan and Alan asked whether the garage door was closed; it was open, and Chorus closed it.",
	}
	if err := r.Keep(context.Background(), []string{"teagan", "", "alan"}, garage); err != nil {
		t.Fatalf("keep: %v", err)
	}
	for _, person := range []string{"teagan", "alan"} {
		got, err := r.Recall(context.Background(), session.Ask{Person: person, ConversationID: "conv-kitchen-0815", Now: friday})
		if err != nil {
			t.Fatalf("recall %s: %v", person, err)
		}
		if !reflect.DeepEqual(got.Summaries, []journal.Summary{garage}) {
			t.Errorf("%s recalls %+v, want the garage door", person, got.Summaries)
		}
	}
	alice, err := r.Recall(context.Background(), session.Ask{Person: "alice", ConversationID: "conv-office-0815", Now: friday})
	if err != nil {
		t.Fatalf("recall alice: %v", err)
	}
	if alice.Summaries != nil {
		t.Errorf("alice, who was not there, recalls %+v", alice.Summaries)
	}
}

// "What did I ask yesterday" gets the past week, newest first, and not the
// conversation it is asked in.
//
// verifies SPEC §5
func TestTheRecallerGivesTheLastWeeksConversations(t *testing.T) {
	store := memory.NewMemStore()
	keepAll(t, store, teagansWeek())
	got, err := memory.Recaller(store, memory.RecallConfig{}).Recall(context.Background(), session.Ask{Person: "teagan", ConversationID: "conv-office-0702", Now: friday})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	var ids []string
	for _, s := range got.Summaries {
		ids = append(ids, s.ConversationID)
	}
	if want := []string{"conv-garage-0812", "conv-kitchen-0731", "conv-front-door-0611"}; !reflect.DeepEqual(ids, want) {
		t.Errorf("recalled %v, want %v", ids, want)
	}
}
