package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// exportable is the one rule of the dataset: accepted, fixed, and attributed
// to a configuration a trainer can hold responsible.
func exportable(p pair) bool { return p.Exportable() && p.H.Attributed }

// dataset is the curated corpus as harvest.Export writes it: each exportable
// candidate with the reviewer's chosen side applied. Rows are derived on
// read (ADR-0026, ADR-0034); nothing stores them.
func dataset(pairs []pair) []harvest.Pair {
	var out []harvest.Pair
	for _, p := range pairs {
		if !exportable(p) {
			continue
		}
		h := p.H
		h.Chosen, h.Curated = p.Chosen, true
		out = append(out, h)
	}
	return out
}

// curated reads the dataset under the lock and returns it detached, so a
// slow client streaming it holds nothing the other pages need.
func (s *server) curated(r *http.Request) ([]pair, []harvest.Pair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairs, err := s.pairs(r.Context())
	if err != nil {
		return nil, nil, err
	}
	return pairs, dataset(pairs), nil
}

// exportJSONL serves the dataset in harvest.Export's conversational shape.
func (s *server) exportJSONL(w http.ResponseWriter, r *http.Request) {
	_, rows, err := s.curated(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", `attachment; filename="chorus-dpo.jsonl"`)
	// Headers are sent; a mid-stream failure can only truncate the body.
	_ = harvest.Export(w, rows)
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

// previewRows is how many rows the page shows before the download.
const previewRows = 3

// preview renders the first rows exactly as the download writes them,
// indented for reading.
func preview(rows []harvest.Pair) (string, error) {
	var raw bytes.Buffer
	if err := harvest.Export(&raw, rows[:min(len(rows), previewRows)]); err != nil {
		return "", err
	}
	var out bytes.Buffer
	dec := json.NewDecoder(&raw)
	for dec.More() {
		var line json.RawMessage
		if err := dec.Decode(&line); err != nil {
			return "", err
		}
		if err := json.Indent(&out, line, "", "  "); err != nil {
			return "", err
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}

func (s *server) export(w http.ResponseWriter, r *http.Request) {
	pairs, rows, err := s.curated(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	text, err := preview(rows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	c := countExport(pairs)

	data := map[string]any{
		"Doc":    s.doc("Export · DPO dataset"),
		"Header": ui.NewAppHeader(ui.StepExport, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "06 · Export", Title: "DPO dataset",
			Subtitle: "Accepted, fixed and attributed pairs, derived from the journal on every read. Nothing is frozen yet; the file is the current state of curation.",
			Actions: []ui.Button{{
				Label: "Download " + plural(c.Exportable, "row") + " (JSONL)",
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
		"Preview": text,
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
