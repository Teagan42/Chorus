package harvest_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// Every turn comes back, cut or not, with the prompt a pair cut from it
// would carry: the heard transcript after the turns before it.
//
// verifies SPEC §9.2
func TestScanReturnsEveryTurnWithItsPrompt(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	res := scan(t, store)
	if len(res.Turns) != 2 {
		t.Fatalf("got %d turns, want the album ask and the correction", len(res.Turns))
	}
	cut, answer := res.Turns[0], res.Turns[1]
	if cut.Seq != 2 || cut.Said != "I found three" || cut.Unheard != " albums by that artist" {
		t.Errorf("cut turn = #%d %q + %q", cut.Seq, cut.Said, cut.Unheard)
	}
	if !cut.Attributed || cut.Versions != versions() {
		t.Errorf("cut turn attributed=%v under %+v", cut.Attributed, cut.Versions)
	}
	wantPrompt := []harvest.Message{
		{Role: "user", Content: "play something by zeppelin", Name: "alice"},
		{Role: "assistant", Content: "I found three"},
		{Role: "user", Content: "just the first one", Name: "alice"},
	}
	if !reflect.DeepEqual(answer.Prompt, wantPrompt) {
		t.Errorf("answer's prompt = %+v, want only what Alice heard before it", answer.Prompt)
	}
	if answer.Said != "Playing Led Zeppelin one." || !reflect.DeepEqual(answer.Audio, []string{"blob://tts/s2"}) {
		t.Errorf("answer said %q in %v", answer.Said, answer.Audio)
	}
	// What Alice said is playable too: a repeat is heard as evidence.
	if cut.AskAudio != "blob://mic/1" || answer.AskAudio != "blob://mic/3" {
		t.Errorf("asks recorded in %q and %q", cut.AskAudio, answer.AskAudio)
	}
}

// A pair cut from a turn is keyed so it can never take a barge-in's id, and
// carries the turn as its rejected side.
//
// verifies SPEC §9.2
func TestATurnBecomesTheRejectedSideOfAReviewersPair(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	res := scan(t, store)
	p := res.Turns[0].Pair("conv-1", harvest.SourceAnnotation, "I found three albums. Want the first?")
	if p.ID != "conv-1/2/annotation" || p.ID == res.Pairs[0].ID {
		t.Errorf("id = %q beside the barge-in's %q", p.ID, res.Pairs[0].ID)
	}
	if p.Rejected+p.RejectedUnheard != "I found three albums by that artist" || p.Seq.Prompt != 2 {
		t.Errorf("rejected = %q at #%d", p.Rejected+p.RejectedUnheard, p.Seq.Prompt)
	}
	if p.Source != harvest.SourceAnnotation || p.Chosen != "I found three albums. Want the first?" {
		t.Errorf("source %q chosen %q", p.Source, p.Chosen)
	}
}

// An annotation's labels and a replay's configuration ride in meta, where a
// trainer can filter on them; a barge-in row has neither key.
//
// verifies SPEC §9.2
func TestExportCarriesWhereAReviewersChosenSideCameFrom(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	res := scan(t, store)
	labelled := res.Turns[0].Pair("conv-1", harvest.SourceAnnotation, "I found three albums. Want the first?")
	labelled.Curated, labelled.Labels = true, []string{"misunderstood_intent"}
	replayed := res.Turns[0].Pair("conv-1", harvest.SourceReplay, "Three albums. Led Zeppelin one?")
	replayed.Curated = true
	replayed.ChosenVersions = journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@7"}
	replayed.ChosenCalls = []journal.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","limit":5}`}}

	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{res.Pairs[0], labelled, replayed}); err != nil {
		t.Fatalf("export: %v", err)
	}
	var rows []struct {
		Meta map[string]json.RawMessage `json:"meta"`
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var r struct {
			Meta map[string]json.RawMessage `json:"meta"`
		}
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("decode: %v", err)
		}
		rows = append(rows, r)
	}
	for _, key := range []string{"labels", "chosen_versions", "chosen_calls"} {
		if _, ok := rows[0].Meta[key]; ok {
			t.Errorf("barge-in row carries %s", key)
		}
	}
	if got := string(rows[1].Meta["labels"]); got != `["misunderstood_intent"]` {
		t.Errorf("labels = %s", got)
	}
	if got := string(rows[2].Meta["chosen_versions"]); got != `{"model":"qwen3-32b@1","prompt":"sys@edited","tool_schema":"tools@7","stt":"","tts":""}` {
		t.Errorf("chosen_versions = %s", got)
	}
	if got := string(rows[2].Meta["source"]); got != `"replay"` {
		t.Errorf("source = %s", got)
	}
	if _, ok := rows[2].Meta["chosen_calls"]; !ok {
		t.Error("replay row lost the calls its chosen side would make")
	}
}
