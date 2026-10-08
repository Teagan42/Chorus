package harvest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
)

func happyPath(t *testing.T) harvest.Pair {
	t.Helper()
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn(), []journal.Record{closed("model_ended")},
	))
	return onePair(t, scan(t, store))
}

// decodeLines parses JSONL into generic values, so the comparison is on the
// JSON shape rather than on key order or whitespace.
func decodeLines(t *testing.T, name string, data []byte) []any {
	t.Helper()
	var out []any
	for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var v any
		if err := json.Unmarshal(line, &v); err != nil {
			t.Fatalf("%s line %d: %v", name, i+1, err)
		}
		out = append(out, v)
	}
	return out
}

// The golden file holds the same pair twice: raw, and curated with a chosen
// side the reviewer wrote. The raw row has no chosen key at all.
//
// verifies SPEC §9.1
func TestExportMatchesTheDPOMessageShape(t *testing.T) {
	raw := happyPath(t)
	curated := raw
	curated.Curated = true
	curated.Chosen = "I found three albums by that artist. Playing the first one."

	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{raw, curated}); err != nil {
		t.Fatalf("export: %v", err)
	}
	golden, err := os.ReadFile("testdata/pairs.jsonl")
	if err != nil {
		t.Fatalf("golden: %v", err)
	}

	got, want := decodeLines(t, "export", buf.Bytes()), decodeLines(t, "golden", golden)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("export mismatch\n got: %s\nwant: %s", buf.Bytes(), golden)
	}
}

// verifies SPEC §9.1
func TestExportLeavesChosenOutOfARawCandidate(t *testing.T) {
	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{happyPath(t)}); err != nil {
		t.Fatalf("export: %v", err)
	}
	var r map[string]any
	if err := json.Unmarshal(buf.Bytes(), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, has := r["chosen"]; has {
		t.Errorf("raw candidate carries a chosen side: %s", buf.Bytes())
	}
	if m, _ := r["meta"].(map[string]any); m["curated"] != false {
		t.Errorf("meta.curated = %v, want false", m["curated"])
	}
	// AsSaid stays in meta for the Curate step; it is just not labelled chosen.
	if !strings.Contains(buf.String(), `"as_said":"Playing Led Zeppelin one."`) {
		t.Errorf("as_said missing from meta: %s", buf.Bytes())
	}
}

// verifies SPEC §9.1
func TestExportRefusesACuratedPairWithoutAChosenSide(t *testing.T) {
	p := happyPath(t)
	p.Curated = true
	err := harvest.Export(&bytes.Buffer{}, []harvest.Pair{p})
	if err == nil || !strings.Contains(err.Error(), p.ID) {
		t.Errorf("err = %v; want a refusal naming the pair", err)
	}
}

// verifies SPEC §9.1
func TestExportIsDeterministic(t *testing.T) {
	p := happyPath(t)
	var first, second bytes.Buffer
	if err := harvest.Export(&first, []harvest.Pair{p}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if err := harvest.Export(&second, []harvest.Pair{p}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Errorf("export is not deterministic\n%s\n%s", first.Bytes(), second.Bytes())
	}
}
