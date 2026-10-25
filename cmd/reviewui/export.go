package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// exportable is the one rule of the dataset: accepted, fixed, and attributed
// to a configuration a trainer can hold responsible.
func exportable(p pair) bool { return p.Exportable() && p.H.Attributed }

// defaultValidation is the fraction of conversations an export holds out
// when the download does not say.
const defaultValidation = 0.1

// exportSet is what one read of the journal exports: every pair, for the
// page's piles, and the rows that ship. Rows are derived on read (ADR-0026,
// ADR-0034); nothing stores them.
type exportSet struct {
	pairs     []pair
	rows      []harvest.Pair
	exemplars []harvest.Exemplar

	// heldExemplars are turns labelled good — exemplar that cannot ship:
	// unattributed, silent, or labelled with a fault besides.
	heldExemplars int
}

// contextSource resolves the versions a row ran under to their text: the
// build's and the archive's, then what chorusd recorded beside the journal,
// where a turn that offered the house's MCP tools finds its schema
// (ADR-0101).
func (s *server) contextSource(ctx context.Context) (contexts.Source, error) {
	src := contexts.Default()
	if s.contexts != nil {
		src = s.contexts
	}
	if s.kept == nil {
		return src, nil
	}
	kept, err := s.kept.Recorded(ctx)
	if err != nil {
		return nil, err
	}
	return contexts.Chain{src, kept}, nil
}

// readCorpus reads the dataset under the lock and returns it detached, so a
// slow client streaming it holds nothing the other pages need.
func (s *server) readCorpus(r *http.Request, u *unread) (exportSet, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	convs, err := s.journal.Conversations(r.Context())
	if err != nil {
		return exportSet{}, err
	}
	return s.corpusOf(r.Context(), convs, u)
}

// corpusOf is the corpus over a listing the caller already holds. Each
// curated row carries its verdict and when it was given, a replay's the
// prompt and schema its take ran under, and every row which clips are gone.
func (s *server) corpusOf(ctx context.Context, convs []string, u *unread) (exportSet, error) {
	pairs, err := s.pairsOf(ctx, convs, u)
	if err != nil {
		return exportSet{}, err
	}
	c := exportSet{pairs: pairs}
	for _, conv := range convs {
		d, err := s.logs.of(ctx, s.journal, conv)
		if err != nil || d.scanErr != nil {
			continue // pairsOf named it skipped
		}
		verdicts, err := s.decisions.ForConversation(ctx, conv)
		if err != nil {
			return exportSet{}, err
		}
		promos, err := s.decisions.Promotions(ctx, conv)
		if err != nil {
			return exportSet{}, err
		}
		for _, p := range pairs {
			if p.conversationID != conv || !exportable(p) {
				continue
			}
			h := p.H
			h.Chosen, h.Curated = p.Chosen, true
			h.Verdict, h.DecidedAt = string(p.Status), verdicts[h.ID].DecidedAt
			if h.Source == harvest.SourceReplay {
				pr := promos[h.Seq.Prompt]
				h.ChosenPrompt, h.ChosenToolSchema = pr.SystemPrompt, pr.ToolSchema
			}
			// A reviewer's pair names an ask said again after the walk.
			h.Audio.Mark(d.scan.Gone)
			c.rows = append(c.rows, h)
		}
		annos, err := s.decisions.Annotations(ctx, conv)
		if err != nil {
			return exportSet{}, err
		}
		for _, t := range d.scan.Turns {
			a, ok := annos[t.Seq]
			if !ok || !a.Has(curation.LabelExemplar) {
				continue
			}
			if !t.Attributed || slices.ContainsFunc(a.Labels, curation.Label.Fault) || silent(t) {
				c.heldExemplars++
				continue
			}
			c.exemplars = append(c.exemplars, harvest.Exemplar{ConversationID: conv, Turn: t, DecidedAt: a.AnnotatedAt})
		}
	}
	return c, nil
}

// silent is a turn that neither said nor called anything: nothing to learn.
func silent(t harvest.Turn) bool {
	if strings.TrimSpace(t.Said+t.Unheard) != "" {
		return false
	}
	return !slices.ContainsFunc(t.Calls, func(c journal.Call) bool { return c.Tool != "speak" })
}

// dataset builds the corpus's rows, split by the download's fraction.
func (s *server) dataset(ctx context.Context, c exportSet, validation float64) (harvest.Dataset, error) {
	src, err := s.contextSource(ctx)
	if err != nil {
		return harvest.Dataset{}, err
	}
	return harvest.Build(c.rows, c.exemplars, harvest.Options{Contexts: src, Validation: validation})
}

// exportQuery is a download's validation fraction and the side of the split
// it asks for, or a sentence saying what it got wrong.
func exportQuery(r *http.Request) (validation float64, split string, err error) {
	validation = defaultValidation
	if v := r.URL.Query().Get("validation"); v != "" {
		validation, err = strconv.ParseFloat(v, 64)
		if err != nil || validation < 0 || validation > 1 {
			return 0, "", fmt.Errorf("validation=%q is not a fraction from 0 to 1", v)
		}
	}
	switch split = r.URL.Query().Get("split"); split {
	case "", harvest.SplitTrain, harvest.SplitValidation:
		return validation, split, nil
	}
	return 0, "", fmt.Errorf("split=%q is neither train nor validation", split)
}

// download reads the corpus and builds the dataset a download asks for,
// answering the request itself when it cannot.
func (s *server) download(w http.ResponseWriter, r *http.Request) (harvest.Dataset, string, bool) {
	validation, split, err := exportQuery(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return harvest.Dataset{}, "", false
	}
	c, err := s.readCorpus(r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return harvest.Dataset{}, "", false
	}
	d, err := s.dataset(r.Context(), c, validation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return harvest.Dataset{}, "", false
	}
	return d, split, true
}

// filename is a download's name, saying which side of the split it holds.
func filename(stem, split, ext string) string {
	if split != "" {
		stem += "-" + split
	}
	return fmt.Sprintf(`attachment; filename="%s.%s"`, stem, ext)
}

// exportJSONL serves the preference rows in harvest's conversational shape.
func (s *server) exportJSONL(w http.ResponseWriter, r *http.Request) {
	d, split, ok := s.download(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", filename("chorus-dpo", split, "jsonl"))
	if err := d.WriteDPO(w, split); err != nil {
		abortStream("export", err)
	}
}

// exportSFT serves the exemplar rows in the prompt-completion shape.
func (s *server) exportSFT(w http.ResponseWriter, r *http.Request) {
	d, split, ok := s.download(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", filename("chorus-sft", split, "jsonl"))
	if err := d.WriteSFT(w, split); err != nil {
		abortStream("sft export", err)
	}
}

// exportArchive serves the whole dataset as one zip, clips included. It
// is both sides of the split, so a split asked for is refused.
func (s *server) exportArchive(w http.ResponseWriter, r *http.Request) {
	d, split, ok := s.download(w, r)
	if !ok {
		return
	}
	if split != "" {
		http.Error(w, "The archive holds both sides of the split, each in its own file; leave split out.", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", filename("chorus-dataset", "", "zip"))
	if err := d.WriteArchive(r.Context(), w, s.blobs, s.now()); err != nil {
		abortStream("archive export", err)
	}
}

// abortStream gives up on a download that failed mid-stream. The 200 is
// sent; only a dropped connection tells the client the file is short.
func abortStream(what string, err error) {
	log.Printf("reviewui: %s aborted: %v", what, err)
	panic(http.ErrAbortHandler)
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
func preview(d harvest.Dataset) (string, error) {
	var raw bytes.Buffer
	if err := d.Head(previewRows).WriteDPO(&raw, ""); err != nil {
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
	var u unread
	c, wakes, err := s.corpora(r, &u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d, err := s.dataset(r.Context(), c, defaultValidation)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	text, err := preview(d)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n := countExport(c.pairs)
	rows, exemplars := d.Len()

	data := map[string]any{
		"Doc":    s.doc("Export · DPO dataset"),
		"Header": ui.NewAppHeader(ui.StepExport, unreviewedCount(c.pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "06 · Export", Title: "DPO dataset",
			Subtitle: "Accepted, fixed and attributed pairs, derived from the journal on every read, each with the system prompt and tools its turn ran under. Nothing is frozen yet; the file is the current state of curation.",
			Actions: []ui.Button{
				{
					Label: "Download " + plural(rows, "row") + " (JSONL)",
					Href:  "/export/dpo.jsonl", Primary: true, Disabled: rows == 0,
				},
				{
					Label: "Download everything with audio (zip)",
					Href:  "/export/dataset.zip", Disabled: rows+exemplars == 0,
				},
			},
		},
		"Stats": ui.MetricStrip{Label: "Piles", Items: []ui.Metric{
			{Label: "Exportable", Value: fmt.Sprint(n.Exportable), Tone: ui.ToneVoice},
			{Label: "Unreviewed", Value: fmt.Sprint(n.Unreviewed)},
			{Label: "Edited", Value: fmt.Sprint(n.Edited)},
			{Label: "Held · unfixed", Value: fmt.Sprint(n.Unfixed), Tone: ui.ToneConv},
			{Label: "Held · unattributed", Value: fmt.Sprint(n.Unattributed), Tone: ui.ToneConv},
			{Label: "Discarded", Value: fmt.Sprint(n.Discarded)},
		}},
		"Preview":       text,
		"Empty":         (*ui.EmptyState)(nil),
		"Unread":        u.alert(),
		"Split":         fmt.Sprintf("%.0f%%", defaultValidation*100),
		"SchemaVersion": harvest.SchemaVersion,
	}
	data["SFT"] = map[string]any{
		"Download": ui.Button{
			Label: "Download " + plural(exemplars, "exemplar") + " (JSONL)",
			Href:  "/export/sft.jsonl", Disabled: exemplars == 0,
		},
		"Stats": ui.MetricStrip{Label: "SFT exemplars", Items: []ui.Metric{
			{Label: "Ships", Value: fmt.Sprint(exemplars), Tone: ui.ToneVoice},
			{Label: "Held", Value: fmt.Sprint(c.heldExemplars), Tone: ui.ToneConv},
		}},
	}
	wc := countWakes(wakes)
	data["Wake"] = map[string]any{
		"Download": ui.Button{
			Label: "Download " + plural(wc.Ships, "negative") + " (JSONL)",
			Href:  "/export/wake-negatives.jsonl", Disabled: wc.Ships == 0,
		},
		"Stats": ui.MetricStrip{Label: "Wake corpus", Items: []ui.Metric{
			{Label: "Ships", Value: fmt.Sprint(wc.Ships), Tone: ui.ToneVoice},
			{Label: "Confirmed", Value: fmt.Sprint(wc.Confirmed)},
			{Label: "Held · unknown speaker", Value: fmt.Sprint(wc.Held), Tone: ui.ToneConv},
			{Label: "Discarded", Value: fmt.Sprint(wc.Discarded)},
		}},
	}
	if rows == 0 {
		data["Empty"] = &ui.EmptyState{
			Title: "Nothing to export yet.",
			Body:  "Accept pairs on the Curate screen; the fixed ones land here.",
		}
	}
	s.render(w, "page-export", data)
}

// corpora reads the corpus and the rejected wakes under one lock and one
// listing of the journal: the Export page walks the log once, not twice.
func (s *server) corpora(r *http.Request, u *unread) (exportSet, []negative, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	convs, err := s.journal.Conversations(r.Context())
	if err != nil {
		return exportSet{}, nil, err
	}
	c, err := s.corpusOf(r.Context(), convs, u)
	if err != nil {
		return exportSet{}, nil, err
	}
	wakes, err := s.negativesOf(r.Context(), convs, u)
	if err != nil {
		return exportSet{}, nil, err
	}
	return c, wakes, nil
}
