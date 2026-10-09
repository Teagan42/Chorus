package rerun_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/rerun"
)

// Teagan's garage door turns, told what Teagan remembers: the first turn
// recorded the recall, and the second was told the same without recording
// it again.
var garageMemories = []journal.Memory{
	{ID: "m_9d1e4b77", Person: "teagan", Fact: "Leaves the garage door open on Saturday mornings for the bins."},
	{ID: "m_77d01b2e", Person: "alice", Fact: "The guest wifi password is on the fridge.", Shareable: true},
}

// A question answered from memory is asked again with that memory. Without
// it, "how do I take my coffee" re-runs as a different question and reports
// drift that is not there.
//
// verifies SPEC §5, §9.2
func TestARerunIsToldWhatTheTurnRemembered(t *testing.T) {
	// The recall lands where the session writes it: after the first
	// utterance, before its first ask.
	recs := garageRecords()
	recalled := record(journal.KindMemoryRecalled, "person", "teagan", "memories_json", journal.EncodeMemories(garageMemories))
	recs = append(recs[:2], append([]journal.Record{recalled}, recs[2:]...)...)
	ts := turns(t, logOf(t, recs))
	if len(ts) != 2 {
		t.Fatalf("got %d turns, want 2", len(ts))
	}
	for i, turn := range ts {
		if !reflect.DeepEqual(turn.Memories, garageMemories) {
			t.Errorf("turn %d remembered %+v, want Teagan's garage and the shared password", i, turn.Memories)
		}
	}
	if plain := turns(t, garageLog(t)); plain[0].Memories != nil {
		t.Errorf("a log with no recall remembered %+v", plain[0].Memories)
	}

	eng := &scripted{}
	if _, err := rerun.Run(context.Background(), eng, "conv-garage", ts[1]); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := eng.asked[0].Memories; !reflect.DeepEqual(got, garageMemories) {
		t.Errorf("the re-run asked with %+v, want what the turn was told", got)
	}
}
