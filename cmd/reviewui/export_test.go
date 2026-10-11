package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
)

// evening is the household's Thursday curated as one sitting: both cuts
// fixed and accepted, the jazz cut discarded as the TV, the Zeppelin turn
// also noted with the same fix, the weather turn re-run under the brief
// prompt and promoted, and the list turn labelled good — exemplar.
func evening(t *testing.T) *server {
	t.Helper()
	s, _ := householdReplayServer(t)
	mustPost(t, s, "/replays/"+convWeather, url.Values{"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt + briefPrompt}})
	mustPost(t, s, fmt.Sprintf("/replays/%s/runs/1/turns/%d/promote", convWeather, weatherAsk), nil)
	mustPost(t, s, turnURL(convZeppel, zeppelinAsk, "labels/misunderstood_intent"), url.Values{"should_have": {fixZeppel}})
	mustPost(t, s, turnURL(convList, 2, "labels/exemplar"), nil)
	for _, step := range []struct {
		id, action string
		form       url.Values
	}{
		{pairWeather, "save", url.Values{"chosen": {fixWeather}}},
		{pairWeather, "accept", nil},
		{pairZeppel, "save", url.Values{"chosen": {fixZeppel}}},
		{pairZeppel, "accept", nil},
		{annotatedZeppel, "accept", nil},
		{pairJazz, "discard", url.Values{"reason": {"Barge-in was noise"}}},
	} {
		if code, h := post(t, s, "/pairs/"+step.id+"/"+step.action, step.form); code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", step.action, step.id, code, h)
		}
	}
	return s
}

// goldenJSONL compares JSONL with a file in testdata by JSON shape.
// CHORUS_UPDATE_GOLDEN=1 rewrites the file instead. Each row's tools come
// from the live registry, which other changes grow, so the file holds a
// placeholder for them; the tests check what they offer by name.
func goldenJSONL(t *testing.T, name, got string) {
	t.Helper()
	var rows []any
	var norm strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, ok := v["tools"]; ok {
			v["tools"] = "the registry's declarations"
		}
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		norm.Write(append(b, '\n'))
		rows = append(rows, any(v))
	}
	path := filepath.Join("testdata", name)
	if os.Getenv("CHORUS_UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(norm.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	var wantRows []any
	for _, line := range strings.Split(strings.TrimSpace(string(want)), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantRows = append(wantRows, any(v))
	}
	// Round-trip the got side, so both are the same Go types.
	b, _ := json.Marshal(rows)
	var gotRows []any
	_ = json.Unmarshal(b, &gotRows)
	if !reflect.DeepEqual(gotRows, wantRows) {
		t.Errorf("export differs from %s; rerun with CHORUS_UPDATE_GOLDEN=1 and read the diff\n got: %s", path, norm.String())
	}
}

// exportedRow is what these tests read of a row.
type exportedRow struct {
	SchemaVersion int `json:"schema_version"`
	Prompt        []struct{ Role, Content string }
	Tools         []struct {
		Function struct{ Name string } `json:"function"`
	} `json:"tools"`
	Meta struct {
		ID         string `json:"id"`
		Source     string `json:"source"`
		Verdict    string `json:"verdict"`
		DecidedAt  string `json:"decided_at"`
		Split      string `json:"split"`
		Duplicates []struct {
			ID string `json:"id"`
		} `json:"duplicates"`
		ContextMissing []string `json:"context_missing"`
		Audio          struct {
			Paths map[string]string `json:"paths"`
		} `json:"audio"`
	} `json:"meta"`
}

func exportedRows(t *testing.T, body string) []exportedRow {
	t.Helper()
	var out []exportedRow
	for _, line := range strings.Split(strings.TrimSpace(body), "\n") {
		if line == "" {
			continue
		}
		var r exportedRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

// The evening's dataset, as a trainer downloads it, is pinned whole: each
// row opens with the persona its turn ran under and carries the tools it
// was offered, the promoted re-run carries the brief prompt and the
// registry's schema it ran under, the Zeppelin note ships once inside the
// cut it duplicates, and the list turn is the one SFT row.
//
// verifies SPEC §9, §9.1, §9.2, §13
func TestTheHouseholdsThursdayExportsAsItsGoldenFiles(t *testing.T) {
	s := evening(t)
	dpo := get(t, s, "/export/dpo.jsonl?validation=0.5")
	goldenJSONL(t, "household-dpo.jsonl", dpo)
	sft := get(t, s, "/export/sft.jsonl?validation=0.5")
	goldenJSONL(t, "household-sft.jsonl", sft)

	rows := exportedRows(t, dpo)
	ids := []string{}
	for _, r := range rows {
		ids = append(ids, r.Meta.ID)
		if r.SchemaVersion != harvest.SchemaVersion || r.Meta.Verdict != "accepted" || r.Meta.DecidedAt == "" || r.Meta.ContextMissing != nil {
			t.Errorf("%s: schema %d verdict %q at %q missing %v", r.Meta.ID, r.SchemaVersion, r.Meta.Verdict, r.Meta.DecidedAt, r.Meta.ContextMissing)
		}
		if r.Prompt[0].Role != "system" || !strings.HasPrefix(r.Prompt[0].Content, "You are a voice assistant in a home.") {
			t.Errorf("%s opens with %+v", r.Meta.ID, r.Prompt[0])
		}
	}
	if want := []string{pairZeppel, pairWeather, replayedWeather}; !slices.Equal(ids, want) {
		t.Fatalf("rows = %v, want %v", ids, want)
	}
	if d := rows[0].Meta.Duplicates; len(d) != 1 || d[0].ID != annotatedZeppel {
		t.Errorf("the Zeppelin cut names duplicates %+v, want the note", d)
	}
	// The day ran under tools@7, which offered media_search; the re-run
	// ran under the registry's schema, which defers it.
	offers := func(r exportedRow, tool string) bool {
		return slices.ContainsFunc(r.Tools, func(x struct {
			Function struct{ Name string } `json:"function"`
		},
		) bool {
			return x.Function.Name == tool
		})
	}
	if !offers(rows[0], "media_search") || offers(rows[2], "media_search") || len(rows[2].Tools) != len(registry.Offered()) {
		t.Errorf("tools: the cut offers media_search %v, the re-run %v with %d tools", offers(rows[0], "media_search"), offers(rows[2], "media_search"), len(rows[2].Tools))
	}
	if rows[2].Prompt[0].Content != ollama.DefaultPrompt+briefPrompt {
		t.Errorf("the re-run's system prompt = %q, want the edit it ran under", rows[2].Prompt[0].Content)
	}
	if ex := exportedRows(t, sft); len(ex) != 1 || ex[0].Meta.ID != convList+"/2/exemplar" || ex[0].Meta.Source != "exemplar" {
		t.Errorf("sft rows = %+v, want the list turn", ex)
	}
}

// One side of the split downloads on its own, and the two sides are the
// whole file; a fraction or side the export cannot take is refused in words.
//
// verifies SPEC §9.1
func TestADownloadTakesTheSplitItAsksFor(t *testing.T) {
	s := evening(t)
	all := exportedRows(t, get(t, s, "/export/dpo.jsonl?validation=0.5"))
	train := exportedRows(t, get(t, s, "/export/dpo.jsonl?validation=0.5&split=train"))
	val := exportedRows(t, get(t, s, "/export/dpo.jsonl?validation=0.5&split=validation"))
	if len(train)+len(val) != len(all) {
		t.Errorf("train %d + validation %d of %d rows", len(train), len(val), len(all))
	}
	for _, r := range val {
		if r.Meta.Split != harvest.SplitValidation {
			t.Errorf("%s in validation says %s", r.Meta.ID, r.Meta.Split)
		}
	}
	for _, target := range []string{
		"/export/dpo.jsonl?validation=2", "/export/sft.jsonl?validation=half",
		"/export/dpo.jsonl?split=test", "/export/dataset.zip?split=train",
	} {
		rec := httptest.NewRecorder()
		s.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", target, rec.Code)
		}
	}
}

// The zip is the evening's dataset off the box: both files of each kind,
// the manifest, and every clip a row names as a WAV at the path the row
// gives for it.
//
// verifies SPEC §8, §9.2
func TestTheArchiveDownloadHoldsTheEveningsRowsAndClips(t *testing.T) {
	s := evening(t)
	rec := httptest.NewRecorder()
	s.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/export/dataset.zip", nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), `filename="chorus-dataset.zip"`) {
		t.Fatalf("archive = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Content-Disposition"))
	}
	z, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range z.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name], err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"manifest.json", "dpo/train.jsonl", "dpo/validation.jsonl", "sft/train.jsonl", "sft/validation.jsonl"} {
		if _, ok := files[name]; !ok {
			t.Errorf("the archive has no %s", name)
		}
	}
	var m struct {
		ExportedAt string         `json:"exported_at"`
		Rows       map[string]int `json:"rows"`
		Clips      int            `json:"clips"`
	}
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.ExportedAt != "2025-10-09T22:30:00Z" || m.Rows["dpo/train.jsonl"]+m.Rows["dpo/validation.jsonl"] != 3 ||
		m.Rows["sft/train.jsonl"]+m.Rows["sft/validation.jsonl"] != 1 || m.Clips == 0 {
		t.Errorf("manifest = %+v", m)
	}
	rows := exportedRows(t, string(files["dpo/train.jsonl"])+"\n"+string(files["dpo/validation.jsonl"]))
	clips := 0
	for _, r := range rows {
		for ref, path := range r.Meta.Audio.Paths {
			clips++
			if wav := files[path]; len(wav) < 44 || string(wav[:4]) != "RIFF" {
				t.Errorf("%s's clip %s is not a WAV at %s", r.Meta.ID, ref, path)
			}
		}
	}
	if clips == 0 {
		t.Error("no row points at a clip in the archive")
	}
}

// The page offers the archive and the SFT file beside the DPO download,
// and counts the exemplar that ships.
//
// verifies SPEC §9.2
func TestTheExportPageOffersTheArchiveAndTheExemplars(t *testing.T) {
	h := get(t, evening(t), "/export")
	for _, want := range []string{
		`href="/export/dataset.zip"`, `href="/export/sft.jsonl"`,
		"Download 3 rows (JSONL)", "Download 1 exemplar (JSONL)", "SFT exemplars", "Format 2.",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the export page is missing %s", want)
		}
	}
}
