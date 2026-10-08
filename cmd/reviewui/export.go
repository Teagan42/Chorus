package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
)

// dpoRow is one training example. The export is derived on read from the
// journal and the verdicts (ADR-0026, ADR-0034), so the format can evolve:
// nothing stores these rows.
type dpoRow struct {
	// Prompt is the shared context both sides answer, chat-shaped, ending
	// with the user utterance the rejected turn replied to.
	Prompt []harvest.Message `json:"prompt"`
	// Chosen is the reviewer's fixed answer to that prompt; Rejected is the
	// turn as generated, the unheard tail included (SPEC §9.1).
	Chosen   string  `json:"chosen"`
	Rejected string  `json:"rejected"`
	Meta     dpoMeta `json:"meta"`
}

type dpoMeta struct {
	Pair         string      `json:"pair"`
	Conversation string      `json:"conversation"`
	Source       string      `json:"source"`
	Versions     dpoVersions `json:"versions"`
}

// dpoVersions pins the dataset's key spelling: journal.Versions has no JSON
// tags, and a training file must not change shape when that struct does.
type dpoVersions struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	ToolSchema string `json:"tool_schema"`
	STT        string `json:"stt,omitempty"`
	TTS        string `json:"tts,omitempty"`
}

func versionsOf(v journal.Versions) dpoVersions {
	return dpoVersions{Model: v.Model, Prompt: v.Prompt, ToolSchema: v.ToolSchema, STT: v.STT, TTS: v.TTS}
}

// exportable is the one rule of the dataset: accepted, fixed, and attributed
// to a configuration a trainer can hold responsible.
func exportable(p pair) bool { return p.Exportable() && p.H.Attributed }

func dpoRowOf(p pair) dpoRow {
	return dpoRow{
		Prompt:   p.H.Prompt,
		Chosen:   p.Chosen,
		Rejected: p.H.Rejected + p.H.RejectedUnheard,
		Meta: dpoMeta{
			Pair: p.ID, Conversation: p.conversationID,
			Source: string(p.H.Source), Versions: versionsOf(p.H.Versions),
		},
	}
}

// exportJSONL serves the dataset: one row per exportable pair, in log order.
func (s *server) exportJSONL(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pairs, err := s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", `attachment; filename="chorus-dpo.jsonl"`)
	enc := json.NewEncoder(w)
	for _, p := range pairs {
		if !exportable(p) {
			continue
		}
		if err := enc.Encode(dpoRowOf(p)); err != nil {
			return
		}
	}
}

// exportCounts are the piles the page explains: what ships and what each
// gate is holding back.
type exportCounts struct {
	Exportable, Unreviewed, Edited, Discarded, Unfixed, Unattributed int
}

func countExport(pairs []pair) exportCounts {
	var c exportCounts
	for _, p := range pairs {
		switch {
		case exportable(p):
			c.Exportable++
		case p.Status == ui.PairAccepted && p.Unfixed:
			c.Unfixed++
		case p.Status == ui.PairAccepted && !p.H.Attributed:
			c.Unattributed++
		case p.Status == ui.PairEdited:
			c.Edited++
		case p.Status == ui.PairDiscarded:
			c.Discarded++
		default:
			c.Unreviewed++
		}
	}
	return c
}

func (s *server) export(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pairs, err := s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c := countExport(pairs)

	var preview strings.Builder
	n := 0
	for _, p := range pairs {
		if !exportable(p) || n == 3 {
			continue
		}
		b, err := json.MarshalIndent(dpoRowOf(p), "", "  ")
		if err != nil {
			continue
		}
		preview.Write(b)
		preview.WriteString("\n")
		n++
	}

	data := map[string]any{
		"Doc":    ui.Doc{Title: "Export · DPO dataset", Static: "/static"},
		"Header": ui.NewAppHeader(ui.StepExport, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "06 · Export", Title: "DPO dataset",
			Subtitle: "Accepted, fixed and attributed pairs, derived from the journal on every read. Nothing is frozen yet; the file is the current state of curation.",
			Actions: []ui.Button{{
				Label: fmt.Sprintf("Download %d rows (JSONL)", c.Exportable),
				Href:  "/export/dpo.jsonl", Primary: true, Disabled: c.Exportable == 0,
			}},
		},
		"Stats": ui.MetricStrip{Label: "Piles", Items: []ui.Metric{
			{Label: "Exportable", Value: fmt.Sprint(c.Exportable), Tone: ui.ToneVoice},
			{Label: "Unreviewed", Value: fmt.Sprint(c.Unreviewed)},
			{Label: "Edited", Value: fmt.Sprint(c.Edited)},
			{Label: "Held · unfixed", Value: fmt.Sprint(c.Unfixed), Tone: ui.ToneConv},
			{Label: "Held · unattributed", Value: fmt.Sprint(c.Unattributed), Tone: ui.ToneConv},
			{Label: "Discarded", Value: fmt.Sprint(c.Discarded)},
		}},
		"Preview": preview.String(),
		"Empty":   (*ui.EmptyState)(nil),
	}
	if c.Exportable == 0 {
		data["Empty"] = &ui.EmptyState{
			Title: "Nothing to export yet.",
			Body:  "Accept pairs on the Curate screen; the fixed ones land here.",
		}
	}
	s.render(w, "page-export", data)
}
