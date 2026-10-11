package harvest_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// exportedAt is the evening the reviewer downloads the day's dataset.
var exportedAt = time.Date(2025, time.October, 9, 22, 45, 0, 0, time.UTC)

// v2 is a row as a trainer's loader reads it, DPO or SFT.
type v2 struct {
	SchemaVersion int               `json:"schema_version"`
	Prompt        []harvest.Message `json:"prompt"`
	Chosen        []harvest.Message `json:"chosen"`
	Rejected      []harvest.Message `json:"rejected"`
	Completion    []harvest.Message `json:"completion"`
	Tools         json.RawMessage   `json:"tools"`
	Meta          struct {
		ID             string   `json:"id"`
		ConversationID string   `json:"conversation_id"`
		Source         string   `json:"source"`
		Verdict        string   `json:"verdict"`
		DecidedAt      string   `json:"decided_at"`
		Labels         []string `json:"labels"`
		Split          string   `json:"split"`
		Context        struct {
			Prompt     string `json:"prompt"`
			ToolSchema string `json:"tool_schema"`
		} `json:"context"`
		ContextMissing []string `json:"context_missing"`
		Duplicates     []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"duplicates"`
		Audio struct {
			Ask   string            `json:"ask"`
			Gone  []string          `json:"gone"`
			Paths map[string]string `json:"paths"`
		} `json:"audio"`
	} `json:"meta"`
}

func rowsOf(t *testing.T, data []byte) []v2 {
	t.Helper()
	var out []v2
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r v2
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("decode %s: %v", line, err)
		}
		out = append(out, r)
	}
	return out
}

func dpoRows(t *testing.T, d harvest.Dataset, split string) []v2 {
	t.Helper()
	var buf bytes.Buffer
	if err := d.WriteDPO(&buf, split); err != nil {
		t.Fatal(err)
	}
	return rowsOf(t, buf.Bytes())
}

func build(t *testing.T, pairs []harvest.Pair, ex []harvest.Exemplar, opt harvest.Options) harvest.Dataset {
	t.Helper()
	d, err := harvest.Build(pairs, ex, opt)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return d
}

// accepted is the barge-in pair as a reviewer accepted it, with the chosen
// side that answers the album request.
func accepted(t *testing.T) harvest.Pair {
	p := happyPath(t)
	p.Curated, p.Chosen = true, "I found three albums. Want Led Zeppelin one?"
	p.Verdict, p.DecidedAt = "accepted", exportedAt.Add(-15*time.Minute)
	return p
}

// briefPrompt is the reviewer's edit the re-run ran under.
const briefPrompt = persona + " Lead with the count, offer the first, and stop."

// The promoted re-run ran under the reviewer's own prompt and a schema
// without speak's text description, and its row carries both, not the
// recorded turn's: the chosen side is only right against them.
//
// verifies SPEC §9.2, §13
func TestAPromotedReRunCarriesTheEditedPromptAndSchemaItRanUnder(t *testing.T) {
	res := scan(t, conversation(t, versions(), concat([]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn())))
	edited := `[{"type":"function","function":{"name":"media_search","description":"Find music. Ask for at most five.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}}]`
	replay := res.Turns[0].Pair("conv-1", harvest.SourceReplay, "Three albums. Led Zeppelin one?")
	replay.Curated, replay.Verdict, replay.DecidedAt = true, "accepted", exportedAt
	replay.ChosenVersions = journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@edited"}
	replay.ChosenPrompt, replay.ChosenToolSchema = briefPrompt, edited
	replay.ChosenCalls = []journal.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin"}`}}

	rows := dpoRows(t, build(t, []harvest.Pair{replay}, nil, harvest.Options{Contexts: fixtureContexts()}), "")
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the replay", len(rows))
	}
	r := rows[0]
	if r.Prompt[0].Role != "system" || r.Prompt[0].Content != briefPrompt {
		t.Errorf("prompt opens with %+v, want the edited system prompt", r.Prompt[0])
	}
	if string(r.Tools) != edited {
		t.Errorf("tools = %s, want the edited schema", r.Tools)
	}
	if r.Meta.Context.Prompt != "sys@edited" || r.Meta.Context.ToolSchema != "tools@edited" || r.Meta.ContextMissing != nil {
		t.Errorf("context = %+v missing %v, want the re-run's versions, both held", r.Meta.Context, r.Meta.ContextMissing)
	}

	// Promoted before the schema was kept beside the prompt (ADR-0059): the
	// label resolves from the archive, here the fixtures' table.
	replay.ChosenToolSchema = ""
	replay.ChosenVersions.ToolSchema = "tools@7"
	r = dpoRows(t, build(t, []harvest.Pair{replay}, nil, harvest.Options{Contexts: fixtureContexts()}), "")[0]
	if !strings.Contains(string(r.Tools), `"speak"`) || r.Prompt[0].Content != briefPrompt {
		t.Errorf("an older promotion carries tools %s and prompt %q", r.Tools, r.Prompt[0].Content)
	}
}

// A turn recorded under a prompt nobody archived ships with what is known
// and says what is not: no system message is invented for it, and no
// neighbouring version's tools stand in.
//
// verifies SPEC §13
func TestARowWhoseContextIsArchivedNowhereSaysSo(t *testing.T) {
	p := accepted(t)
	p.Versions.Prompt = "sys@2"
	r := dpoRows(t, build(t, []harvest.Pair{p}, nil, harvest.Options{Contexts: fixtureContexts()}), "")[0]
	if !slices.Equal(r.Meta.ContextMissing, []string{"prompt"}) {
		t.Errorf("context_missing = %v, want the prompt alone", r.Meta.ContextMissing)
	}
	if r.Prompt[0].Role == "system" {
		t.Errorf("a system message was guessed: %q", r.Prompt[0].Content)
	}
	if r.Meta.Context.Prompt != "sys@2" || !strings.Contains(string(r.Tools), "media_search") {
		t.Errorf("context %+v tools %s; the known half still ships", r.Meta.Context, r.Tools)
	}

	// No source at all: both halves missing, and the row still exports.
	r = dpoRows(t, build(t, []harvest.Pair{accepted(t)}, nil, harvest.Options{}), "")[0]
	if !slices.Equal(r.Meta.ContextMissing, []string{"prompt", "tool_schema"}) || r.Tools != nil {
		t.Errorf("context_missing = %v tools %s with no source", r.Meta.ContextMissing, r.Tools)
	}
}

// Alice's Zeppelin turn was cut, and the reviewer both accepted the cut's
// pair and noted the turn with the same answer. That is one preference, so
// it ships once: as the barge-in, naming the note and carrying its label.
// The re-run the reviewer promoted chose differently, and ships beside it.
//
// verifies SPEC §9.1, §9.2
func TestTheSamePreferenceFromTwoSourcesShipsOnce(t *testing.T) {
	res := scan(t, conversation(t, versions(), concat([]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn())))
	cut := accepted(t)
	note := res.Turns[0].Pair("conv-1", harvest.SourceAnnotation, "I found three albums.  Want Led Zeppelin one?\n")
	note.Curated, note.Verdict, note.Labels = true, "accepted", []string{"misunderstood_intent"}
	replay := res.Turns[0].Pair("conv-1", harvest.SourceReplay, "Three albums. Led Zeppelin one?")
	replay.Curated, replay.Verdict = true, "accepted"
	replay.ChosenVersions = versions()

	rows := dpoRows(t, build(t, []harvest.Pair{note, cut, replay}, nil, harvest.Options{Contexts: fixtureContexts()}), "")
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want the cut and the replay", len(rows))
	}
	kept := rows[0]
	if kept.Meta.Source != "barge-in" || kept.Meta.ID != "conv-1/7" {
		t.Errorf("kept %s %s, want the barge-in", kept.Meta.Source, kept.Meta.ID)
	}
	if len(kept.Meta.Duplicates) != 1 || kept.Meta.Duplicates[0].ID != "conv-1/2/annotation" || kept.Meta.Duplicates[0].Source != "annotation" {
		t.Errorf("duplicates = %+v, want the note", kept.Meta.Duplicates)
	}
	if !slices.Equal(kept.Meta.Labels, []string{"misunderstood_intent"}) {
		t.Errorf("labels = %v, want the note's", kept.Meta.Labels)
	}
	if rows[1].Meta.Source != "replay" || rows[1].Meta.Duplicates != nil {
		t.Errorf("second row = %s with duplicates %+v", rows[1].Meta.Source, rows[1].Meta.Duplicates)
	}
	if note.Labels[0] != "misunderstood_intent" || cut.Labels != nil {
		t.Error("dedupe changed the pairs it was given")
	}
}

// household is a month of the household's conversations, each with the
// accepted Zeppelin pair, so the split has enough to divide.
func household(t *testing.T, n int) []harvest.Pair {
	t.Helper()
	base := accepted(t)
	var out []harvest.Pair
	for i := range n {
		conv := fmt.Sprintf("conv-%02d%02d-kitchen", 10+i/60, i%60)
		for _, src := range []harvest.Source{harvest.SourceBargeIn, harvest.SourceReplay} {
			p := base
			p.ConversationID, p.Source = conv, src
			p.ID = conv + "/7/" + string(src)
			p.ChosenVersions = versions()
			out = append(out, p)
		}
	}
	return out
}

// The split is by conversation, from its id alone: both rows of a
// conversation land together, the same way every time, and raising the
// fraction only moves conversations into validation.
//
// verifies SPEC §9.1
func TestTheSplitIsByConversationAndDeterministic(t *testing.T) {
	pairs := household(t, 200)
	sides := func(fraction float64) map[string]string {
		got := map[string]string{}
		for _, r := range dpoRows(t, build(t, pairs, nil, harvest.Options{Validation: fraction}), "") {
			if s, ok := got[r.Meta.ConversationID]; ok && s != r.Meta.Split {
				t.Fatalf("%s straddles the split", r.Meta.ConversationID)
			}
			got[r.Meta.ConversationID] = r.Meta.Split
		}
		return got
	}
	quarter := sides(0.25)
	if again := sides(0.25); fmt.Sprint(again) != fmt.Sprint(quarter) {
		t.Error("the same fraction split the same conversations differently")
	}
	held := 0
	for _, s := range quarter {
		if s == harvest.SplitValidation {
			held++
		}
	}
	if held < 30 || held > 70 {
		t.Errorf("a quarter held out %d of 200 conversations", held)
	}
	for conv, s := range sides(0.5) {
		if quarter[conv] == harvest.SplitValidation && s != harvest.SplitValidation {
			t.Errorf("%s left validation when the fraction rose", conv)
		}
	}
	for conv, s := range sides(0) {
		if s != harvest.SplitTrain {
			t.Errorf("%s held out at fraction 0", conv)
		}
	}
	for conv, s := range sides(1) {
		if s != harvest.SplitValidation {
			t.Errorf("%s trained on at fraction 1", conv)
		}
	}
}

// Asking for one side writes only that side's rows, and the two sides are
// the whole dataset.
//
// verifies SPEC §9.1
func TestEachSideOfTheSplitIsItsOwnFile(t *testing.T) {
	d := build(t, household(t, 40), nil, harvest.Options{Validation: 0.3})
	train, val, all := dpoRows(t, d, harvest.SplitTrain), dpoRows(t, d, harvest.SplitValidation), dpoRows(t, d, "")
	if len(train)+len(val) != len(all) || len(train) == 0 || len(val) == 0 {
		t.Fatalf("train %d + validation %d of %d", len(train), len(val), len(all))
	}
	for _, r := range train {
		if r.Meta.Split != harvest.SplitTrain {
			t.Errorf("%s in train says %s", r.Meta.ID, r.Meta.Split)
		}
	}
}

// A fraction outside 0 to 1 is refused rather than read as everything or
// nothing.
//
// verifies SPEC §9.1
func TestAValidationFractionOutsideZeroToOneIsRefused(t *testing.T) {
	for _, f := range []float64{-0.1, 1.5} {
		if _, err := harvest.Build(nil, nil, harvest.Options{Validation: f}); err == nil {
			t.Errorf("fraction %v was taken", f)
		}
	}
}

// exemplar is Alice's corrected turn, which the reviewer marked good —
// exemplar: "just the first one", answered with the first album.
func exemplar(t *testing.T) harvest.Exemplar {
	res := scan(t, conversation(t, versions(), concat([]journal.Record{opened("kitchen")}, cutTurn(), correctedTurn())))
	return harvest.Exemplar{ConversationID: "conv-1", Turn: res.Turns[1], DecidedAt: exportedAt.Add(-5 * time.Minute)}
}

// An exemplar is an SFT row: what the turn was asked, in context, opened by
// its system prompt, and what it said as the completion. It never enters
// the DPO file, which has no rejected side to give it.
//
// verifies SPEC §9.2
func TestAnExemplarIsAnSFTRow(t *testing.T) {
	d := build(t, []harvest.Pair{accepted(t)}, []harvest.Exemplar{exemplar(t)}, harvest.Options{Contexts: fixtureContexts()})
	if dpo, sft := d.Len(); dpo != 1 || sft != 1 {
		t.Fatalf("dataset holds %d DPO and %d SFT rows", dpo, sft)
	}
	var buf bytes.Buffer
	if err := d.WriteSFT(&buf, ""); err != nil {
		t.Fatal(err)
	}
	golden(t, "exemplars.jsonl", buf.Bytes())
	r := rowsOf(t, buf.Bytes())[0]
	if r.Meta.Source != "exemplar" || r.Meta.Verdict != "exemplar" || r.Meta.DecidedAt != "2025-10-09T22:40:00Z" {
		t.Errorf("provenance = %s %s %s", r.Meta.Source, r.Meta.Verdict, r.Meta.DecidedAt)
	}
	if r.Prompt[0].Role != "system" || r.Prompt[len(r.Prompt)-1].Content != "just the first one" {
		t.Errorf("prompt = %+v", r.Prompt)
	}
	if len(r.Completion) != 1 || r.Completion[0].Content != "Playing Led Zeppelin one." || r.Chosen != nil || r.Rejected != nil {
		t.Errorf("completion = %+v (chosen %v, rejected %v)", r.Completion, r.Chosen, r.Rejected)
	}
}

// An exemplar that neither said nor called anything teaches nothing, and is
// refused by name.
//
// verifies SPEC §9.2
func TestASilentExemplarIsRefused(t *testing.T) {
	e := exemplar(t)
	e.Turn.Said, e.Turn.Calls = "", nil
	if _, err := harvest.Build(nil, []harvest.Exemplar{e}, harvest.Options{}); err == nil || !strings.Contains(err.Error(), "conv-1/10/exemplar") {
		t.Errorf("err = %v, want a refusal naming the exemplar", err)
	}
}

// clips is the blob store with the day's clips but the cut speech, which
// retention took before the log could say so. Alice's first ask is kept
// too, though no row names it.
func clips(t *testing.T) *blob.Memory {
	t.Helper()
	m := blob.NewMemory()
	for key, frames := range map[string]int{"mic/1": 3200, "mic/2": 1600, "mic/3": 2400, "tts/s2": 8000} {
		w, err := m.Create(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(make([]byte, 2*frames)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func unzip(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string][]byte{}
	for _, f := range z.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = b
	}
	return out
}

// The archive is the dataset off the box: the rows split into files, every
// clip they name as a WAV beside them, and each row pointing at its clips
// by path. A clip the store no longer has is listed gone, not left dangling.
//
// verifies SPEC §8, §9.2
func TestTheArchiveHoldsTheRowsAndTheirClips(t *testing.T) {
	d := build(t, []harvest.Pair{accepted(t)}, []harvest.Exemplar{exemplar(t)}, harvest.Options{Contexts: fixtureContexts()})
	var buf bytes.Buffer
	if err := d.WriteArchive(context.Background(), &buf, clips(t), exportedAt); err != nil {
		t.Fatalf("archive: %v", err)
	}
	files := unzip(t, buf.Bytes())
	var names []string
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	want := []string{
		"audio/mic/2.wav", "audio/mic/3.wav", "audio/tts/s2.wav",
		"dpo/train.jsonl", "dpo/validation.jsonl", "manifest.json", "sft/train.jsonl", "sft/validation.jsonl",
	}
	if !slices.Equal(names, want) {
		t.Errorf("archive holds %v, want %v", names, want)
	}

	wav := files["audio/tts/s2.wav"]
	if string(wav[:4]) != "RIFF" || string(wav[8:16]) != "WAVEfmt " || binary.LittleEndian.Uint32(wav[24:]) != 16000 ||
		binary.LittleEndian.Uint32(wav[40:]) != 16000 || len(wav) != 44+16000 {
		t.Errorf("tts/s2 is not a 16 kHz WAV of its half second: % x", wav[:44])
	}

	var m struct {
		SchemaVersion int            `json:"schema_version"`
		ExportedAt    string         `json:"exported_at"`
		Rows          map[string]int `json:"rows"`
		Clips         int            `json:"clips"`
		Gone          []string       `json:"gone"`
	}
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.SchemaVersion != harvest.SchemaVersion || m.ExportedAt != "2025-10-09T22:45:00Z" || m.Clips != 3 ||
		!slices.Equal(m.Gone, []string{"blob://tts/s1"}) || m.Rows["dpo/train.jsonl"] != 1 || m.Rows["sft/train.jsonl"] != 1 {
		t.Errorf("manifest = %+v", m)
	}

	dpo := rowsOf(t, files["dpo/train.jsonl"])[0]
	if dpo.Meta.Audio.Paths["blob://mic/2"] != "audio/mic/2.wav" || dpo.Meta.Audio.Paths["blob://tts/s2"] != "audio/tts/s2.wav" {
		t.Errorf("dpo paths = %v", dpo.Meta.Audio.Paths)
	}
	if _, ok := dpo.Meta.Audio.Paths["blob://tts/s1"]; ok || !slices.Equal(dpo.Meta.Audio.Gone, []string{"blob://tts/s1"}) {
		t.Errorf("the missing clip has path %v and gone %v", dpo.Meta.Audio.Paths, dpo.Meta.Audio.Gone)
	}
	sft := rowsOf(t, files["sft/train.jsonl"])[0]
	if sft.Meta.Audio.Paths["blob://mic/3"] != "audio/mic/3.wav" {
		t.Errorf("sft paths = %v", sft.Meta.Audio.Paths)
	}
	if len(files["dpo/validation.jsonl"]) != 0 {
		t.Errorf("validation holds rows at fraction 0: %s", files["dpo/validation.jsonl"])
	}
}

// A clip the log already says retention dropped stays gone in the archive,
// and is not copied even while the store still answers for it.
//
// verifies SPEC §8
func TestAClipTheLogSaysIsGoneStaysGoneInTheArchive(t *testing.T) {
	p := accepted(t)
	p.Audio.Gone = []string{"blob://mic/2"}
	d := build(t, []harvest.Pair{p}, nil, harvest.Options{})
	var buf bytes.Buffer
	if err := d.WriteArchive(context.Background(), &buf, clips(t), exportedAt); err != nil {
		t.Fatal(err)
	}
	r := rowsOf(t, unzip(t, buf.Bytes())["dpo/train.jsonl"])[0]
	if !slices.Equal(r.Meta.Audio.Gone, []string{"blob://mic/2", "blob://tts/s1"}) {
		t.Errorf("gone = %v, want the log's and the store's", r.Meta.Audio.Gone)
	}
	if _, ok := r.Meta.Audio.Paths["blob://mic/2"]; ok {
		t.Errorf("a gone clip has a path: %v", r.Meta.Audio.Paths)
	}
}

// A store that fails for any reason but a missing clip fails the archive:
// a short corpus that looks whole is worse than none.
//
// verifies SPEC §8
func TestAStoreThatFailsFailsTheArchive(t *testing.T) {
	d := build(t, []harvest.Pair{accepted(t)}, nil, harvest.Options{})
	err := d.WriteArchive(context.Background(), io.Discard, brokenClips{}, exportedAt)
	if err == nil || !strings.Contains(err.Error(), "blob://") {
		t.Errorf("err = %v, want the clip named", err)
	}
}

type brokenClips struct{}

func (brokenClips) Open(context.Context, string) (io.ReadCloser, error) {
	return nil, fmt.Errorf("disk read error")
}
