package harvest_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
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
