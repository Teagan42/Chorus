package harvest_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
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

// persona and searchTools stand for what sys@3 and tools@7 name: the
// fixtures' versions, archived nowhere but here.
const (
	persona     = "You are a voice assistant in a home. Everything you say is spoken aloud, so keep replies short and plain."
	searchTools = `[
  {"type": "function", "function": {"name": "media_search", "description": "Find music in the library.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}},
  {"type": "function", "function": {"name": "speak", "description": "Say something out loud.", "parameters": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}}}
]`
)

// fixtureContexts resolves the fixtures' versions, as the archive resolves a
// real build's.
func fixtureContexts() contexts.Table {
	return contexts.Table{
		Prompts:     map[string]string{"sys@3": persona},
		ToolSchemas: map[string]string{"tools@7": searchTools},
	}
}

// golden compares JSONL with a file in testdata, by JSON shape rather than
// key order. CHORUS_UPDATE_GOLDEN=1 rewrites the file instead.
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if os.Getenv("CHORUS_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	if g, w := decodeLines(t, "export", got), decodeLines(t, name, want); !reflect.DeepEqual(g, w) {
		t.Errorf("export differs from %s; rerun with CHORUS_UPDATE_GOLDEN=1 and read the diff\n got: %s\nwant: %s", path, got, want)
	}
}

// The golden file holds the same pair twice: raw, and curated with a chosen
// side the reviewer wrote. The raw row has no chosen key at all. Both open
// with the system prompt the turn ran under and carry the tools it was
// offered, and the curated one says what the reviewer decided and when.
//
// verifies SPEC §9.1, §13
func TestExportMatchesTheDPOMessageShape(t *testing.T) {
	raw := happyPath(t)
	curated := raw
	curated.Curated = true
	curated.Chosen = "I found three albums by that artist. Playing the first one."
	curated.Verdict, curated.DecidedAt = "accepted", time.Date(2025, time.October, 9, 22, 30, 0, 0, time.UTC)

	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{raw, curated}, harvest.Options{Contexts: fixtureContexts()}); err != nil {
		t.Fatalf("export: %v", err)
	}
	golden(t, "pairs.jsonl", buf.Bytes())
}

// verifies SPEC §9.1
func TestExportLeavesChosenOutOfARawCandidate(t *testing.T) {
	var buf bytes.Buffer
	if err := harvest.Export(&buf, []harvest.Pair{happyPath(t)}, harvest.Options{}); err != nil {
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
	err := harvest.Export(&bytes.Buffer{}, []harvest.Pair{p}, harvest.Options{})
	if err == nil || !strings.Contains(err.Error(), p.ID) {
		t.Errorf("err = %v; want a refusal naming the pair", err)
	}
}

// verifies SPEC §9.1
func TestExportIsDeterministic(t *testing.T) {
	p := happyPath(t)
	var first, second bytes.Buffer
	if err := harvest.Export(&first, []harvest.Pair{p}, harvest.Options{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if err := harvest.Export(&second, []harvest.Pair{p}, harvest.Options{}); err != nil {
		t.Fatalf("export: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Errorf("export is not deterministic\n%s\n%s", first.Bytes(), second.Bytes())
	}
}
