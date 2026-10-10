package rerun_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
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

// What Teagan did in the garage on Saturday, as the model summarized it.
var saturdayInTheGarage = []journal.Summary{
	{ConversationID: "conv-garage-sat", At: time.Date(2025, 10, 4, 9, 12, 0, 0, time.UTC), Text: "Teagan asked for the garage door open for the bins and the lights on."},
}

// "Same as Saturday" is a different question on a different day. A re-run
// is asked with the conversations the turn was told of, and told it is the
// time the turn was heard, not the time the re-run happens.
//
// verifies SPEC §5, §8, §9.2
func TestARerunIsToldWhenTheTurnWasHeardAndWhatCameBefore(t *testing.T) {
	recs := garageRecords()
	recalled := record(journal.KindMemoryRecalled, "person", "teagan",
		"memories_json", journal.EncodeMemories(nil), "summaries_json", journal.EncodeSummaries(saturdayInTheGarage))
	recs = append(recs[:2], append([]journal.Record{recalled}, recs[2:]...)...)
	ts := turns(t, logOf(t, recs))
	heardAt := time.Unix(1_760_000_000, 0).UTC()
	for i, turn := range ts {
		if !reflect.DeepEqual(turn.Summaries, saturdayInTheGarage) || !turn.HeardAt.Equal(heardAt) {
			t.Errorf("turn %d was told %+v at %v", i, turn.Summaries, turn.HeardAt)
		}
	}

	eng := &scripted{}
	if _, err := rerun.Run(context.Background(), eng, "conv-garage", ts[0]); err != nil {
		t.Fatalf("run: %v", err)
	}
	if in := eng.asked[0]; !reflect.DeepEqual(in.Summaries, saturdayInTheGarage) || !in.Now.Equal(heardAt) {
		t.Errorf("the re-run asked with %+v at %v, want what the turn was told", in.Summaries, in.Now)
	}
}
