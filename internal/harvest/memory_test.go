package harvest_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// Alice's own memory, and the bins Teagan shared with the house.
var alicesMemories = []journal.Memory{
	{ID: "m_c0ffee42", Person: "alice", Fact: "Likes Led Zeppelin's first album best."},
	{ID: "m_51ab0c3d", Person: "teagan", Fact: "The bins go out on Thursday night.", Shareable: true},
}

func recalled(ms []journal.Memory) journal.Record {
	return record(journal.KindMemoryRecalled, "", "person", "alice", "memories_json", journal.EncodeMemories(ms))
}

// The cut turn was asked knowing Alice likes the first album. A pair
// without that teaches a model to guess it, so the pair carries what the
// turn recalled, and the export says so.
//
// verifies SPEC §5, §9.1
func TestAPairCarriesWhatTheRejectedTurnRemembered(t *testing.T) {
	cut := cutTurn()
	p := onePair(t, scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen"), cut[0], recalled(alicesMemories)}, cut[1:], correctedTurn(),
	))))
	if !reflect.DeepEqual(p.Recalled, alicesMemories) {
		t.Fatalf("pair recalled %+v, want Alice's two", p.Recalled)
	}

	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{p}); err != nil {
		t.Fatalf("export: %v", err)
	}
	var row struct {
		Meta struct {
			Recalled []journal.Memory `json:"recalled"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("decode %s: %v", buf.Bytes(), err)
	}
	if !reflect.DeepEqual(row.Meta.Recalled, alicesMemories) {
		t.Errorf("exported recalled %+v, want Alice's two", row.Meta.Recalled)
	}
}

// Memory that has not changed is not recorded again, so a turn with no
// recall of its own was told the last one.
//
// verifies SPEC §5, §9.1
func TestATurnWithNoRecallOfItsOwnWasToldTheLastOne(t *testing.T) {
	p := onePair(t, scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen"), heard("good morning", "blob://mic/0"), recalled(alicesMemories), completed()},
		cutTurn(), correctedTurn(),
	))))
	if !reflect.DeepEqual(p.Recalled, alicesMemories) {
		t.Errorf("pair recalled %+v, want what the earlier turn recorded", p.Recalled)
	}

	// Nothing remembered anywhere: the export leaves the field out, as it
	// did before memory existed.
	plain := onePair(t, scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(),
	))))
	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{plain}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte(`"recalled"`)) {
		t.Errorf("export names recalled with nothing remembered: %s", buf.Bytes())
	}
}

// What Alice played yesterday evening, as the model summarized it.
var alicesEvening = []journal.Summary{
	{ConversationID: "conv-living-room-1904", At: time.Date(2025, 10, 8, 19, 4, 0, 0, time.UTC), Text: "Alice asked for Led Zeppelin's first album and listened to side one."},
}

// The cut turn was told what Alice played last night and what time it was.
// "The same as yesterday" means nothing without both, so the pair carries
// them and the export says so; a summary written after the conversation is
// nobody's turn.
//
// verifies SPEC §5, §9.1
func TestAPairCarriesTheConversationsAndTimeTheRejectedTurnWasTold(t *testing.T) {
	cut := cutTurn()
	withSummaries := record(journal.KindMemoryRecalled, "", "person", "alice",
		"memories_json", journal.EncodeMemories(nil), "summaries_json", journal.EncodeSummaries(alicesEvening))
	summarized := record(journal.KindConversationSummarized, "", "people_json", `["alice"]`,
		"summary", "Alice asked for the first Led Zeppelin album again and picked side one.")
	p := onePair(t, scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen"), cut[0], withSummaries}, cut[1:], correctedTurn(),
		[]journal.Record{closed("model_ended"), summarized},
	))))
	if !reflect.DeepEqual(p.RecalledSummaries, alicesEvening) {
		t.Errorf("pair recalled %+v, want last night's album", p.RecalledSummaries)
	}
	if want := time.Unix(1_760_000_000, 0).UTC(); !p.HeardAt.Equal(want) {
		t.Errorf("pair heard at %v, want %v", p.HeardAt, want)
	}

	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{p}); err != nil {
		t.Fatalf("export: %v", err)
	}
	var row struct {
		Meta struct {
			Conversations []journal.Summary `json:"recalled_conversations"`
			HeardAt       string            `json:"heard_at"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(buf.Bytes(), &row); err != nil {
		t.Fatalf("decode %s: %v", buf.Bytes(), err)
	}
	if !reflect.DeepEqual(row.Meta.Conversations, alicesEvening) || row.Meta.HeardAt != "2025-10-09T08:53:20Z" {
		t.Errorf("exported %+v at %q", row.Meta.Conversations, row.Meta.HeardAt)
	}
}
