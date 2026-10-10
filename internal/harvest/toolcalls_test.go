package harvest_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// exported is one row as a trainer reads it: each side's message, and meta.
type exported struct {
	Chosen   []harvest.Message `json:"chosen"`
	Rejected []harvest.Message `json:"rejected"`
	Meta     struct {
		SpeechOnly bool `json:"speech_only"`
	} `json:"meta"`
}

func exportRows(t *testing.T, pairs ...harvest.Pair) []exported {
	t.Helper()
	var buf bytes.Buffer
	if err := harvest.Export(&buf, pairs); err != nil {
		t.Fatalf("export: %v", err)
	}
	var rows []exported
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var r exported
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		rows = append(rows, r)
	}
	return rows
}

func callsOf(m harvest.Message) string {
	var parts []string
	for _, c := range m.ToolCalls {
		parts = append(parts, c.Type+":"+c.Function.Name+string(c.Function.Arguments))
	}
	return strings.Join(parts, " ")
}

// aliceAsks is Alice's "play something by zeppelin", which searched the
// library and was cut off reading the hits.
func aliceAsks(t *testing.T) harvest.Turn {
	t.Helper()
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	return scan(t, store).Turns[0]
}

const zeppelinSearch = `function:media_search{"query":"zeppelin"}`

// A note or a correction is about what was said, so its chosen side keeps
// the turn's search; speaking stays the content, never a call.
//
// verifies SPEC §4.1, §9.2
func TestANoteKeepsTheTurnsCallsOnBothSides(t *testing.T) {
	noted := aliceAsks(t).Pair("conv-1", harvest.SourceAnnotation, "I found three albums. Want Led Zeppelin one?")
	noted.Curated, noted.Labels = true, []string{"misunderstood_intent"}

	r := exportRows(t, noted)[0]
	if got := callsOf(r.Rejected[0]); got != zeppelinSearch {
		t.Errorf("rejected calls = %s, want the search alone, not the speak", got)
	}
	if got := callsOf(r.Chosen[0]); got != zeppelinSearch {
		t.Errorf("chosen calls = %s, want the turn's search", got)
	}
	if r.Meta.SpeechOnly {
		t.Error("a row with calls on both sides says speech_only")
	}
}

// "Wrong tool" says the calls were wrong but not which were right, so the
// pair trains on speech alone and says so.
//
// verifies SPEC §9.2
func TestAWrongToolNoteTrainsOnSpeechAlone(t *testing.T) {
	noted := aliceAsks(t).Pair("conv-1", harvest.SourceAnnotation, "Which Zeppelin? Led Zeppelin the band, or the album?")
	noted.Curated, noted.Labels = true, []string{"misunderstood_intent", "wrong_tool"}

	r := exportRows(t, noted)[0]
	if len(r.Rejected[0].ToolCalls)+len(r.Chosen[0].ToolCalls) != 0 {
		t.Errorf("a wrong-tool row carries calls: rejected %s, chosen %s", callsOf(r.Rejected[0]), callsOf(r.Chosen[0]))
	}
	if !r.Meta.SpeechOnly {
		t.Error("a speech-only row does not say so")
	}
}

// A promoted re-run that only searched differently is a pair: its chosen
// side says nothing and calls what the re-run would.
//
// verifies SPEC §9.2
func TestAPromotedReRunThatOnlyCallsIsAPair(t *testing.T) {
	replayed := aliceAsks(t).Pair("conv-1", harvest.SourceReplay, "")
	replayed.Curated = true
	replayed.ChosenCalls = []journal.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","media_type":"album","limit":5}`}}

	r := exportRows(t, replayed)[0]
	if r.Chosen[0].Content != "" || callsOf(r.Chosen[0]) != `function:media_search{"query":"Led Zeppelin","media_type":"album","limit":5}` {
		t.Errorf("chosen = %q + %s", r.Chosen[0].Content, callsOf(r.Chosen[0]))
	}
	if got := callsOf(r.Rejected[0]); got != zeppelinSearch {
		t.Errorf("rejected calls = %s", got)
	}

	silent := replayed
	silent.ChosenCalls = nil
	if err := harvest.Export(&bytes.Buffer{}, []harvest.Pair{silent}); err == nil || !strings.Contains(err.Error(), silent.ID) {
		t.Errorf("a curated re-run that neither says nor calls anything exported: err = %v", err)
	}
	noted := aliceAsks(t).Pair("conv-1", harvest.SourceAnnotation, "")
	noted.Curated, noted.Labels = true, []string{"too_slow"}
	if err := harvest.Export(&bytes.Buffer{}, []harvest.Pair{noted}); err == nil {
		t.Error("an empty note exported on the strength of the turn's own calls")
	}
}

// Arguments a model wrote malformed are kept as it wrote them, a string,
// rather than dropped or invented.
//
// verifies SPEC §8
func TestMalformedArgumentsRideAsTheModelWroteThem(t *testing.T) {
	replayed := aliceAsks(t).Pair("conv-1", harvest.SourceReplay, "Searching for Zeppelin.")
	replayed.Curated = true
	replayed.ChosenCalls = []journal.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin"`}}

	r := exportRows(t, replayed)[0]
	if got := callsOf(r.Chosen[0]); got != `function:media_search"{\"query\":\"Led Zeppelin\""` {
		t.Errorf("chosen calls = %s", got)
	}
}
