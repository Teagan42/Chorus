package main

import (
	"context"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/audio"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

//go:embed pages/*.tmpl
var pagesFS embed.FS

// Journal is the read side the Curate page needs: the log plus the listing
// that finds it conversations.
type Journal interface {
	journal.Store
	journal.Lister
}

// server serves Curate over the real journal: harvested candidates on the
// left, the kit's pair flow on the right, verdicts in the curation store.
type server struct {
	tpl       *template.Template
	journal   Journal
	decisions curation.Store
	blobs     blob.Store
	now       func() time.Time

	// engineFor builds the turn engine Replay asks; nil when no model
	// endpoint is configured, and Replay says so.
	engineFor engineFactory

	// notice says above every screen what kind of instance this is; nil on
	// a household's own review box.
	notice *ui.Notice

	// One reviewer at a time is the household reality; the lock keeps a
	// read-modify-write on a pair from racing itself.
	mu sync.Mutex
}

func newServer(j Journal, d curation.Store, b blob.Store, now func() time.Time) *server {
	return &server{
		tpl:       template.Must(ui.MustTemplates().ParseFS(pagesFS, "pages/*.tmpl")),
		journal:   j,
		decisions: d,
		blobs:     b,
		now:       now,
	}
}

// routes serves every screen behind a cross-origin check: the box trusts its
// network (SPEC §1), so another site's page in the reviewer's browser must
// not post to it.
func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Static()))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ui.Routes[ui.StepCurate], http.StatusSeeOther)
	})
	mux.HandleFunc("GET "+ui.Routes[ui.StepCurate], s.curate)
	mux.HandleFunc("GET "+ui.Routes[ui.StepReview], s.review)
	mux.HandleFunc("GET "+ui.Routes[ui.StepTriage], s.triage)
	mux.HandleFunc("GET "+ui.Routes[ui.StepBrowse], s.browse)
	mux.HandleFunc("GET "+ui.Routes[ui.StepBrowse]+"/{id}", s.conversation)
	mux.HandleFunc("POST "+ui.Routes[ui.StepBrowse]+"/{id}/turns/{seq}/labels/{label}", s.annotate)
	mux.HandleFunc("POST "+ui.Routes[ui.StepBrowse]+"/{id}/turns/{seq}/note", s.annotate)
	mux.HandleFunc("GET "+ui.Routes[ui.StepReplay], s.replays)
	mux.HandleFunc("GET "+ui.Routes[ui.StepReplay]+"/{id}", s.replayPage)
	mux.HandleFunc("POST "+ui.Routes[ui.StepReplay]+"/{id}", s.replayRun)
	mux.HandleFunc("POST "+ui.Routes[ui.StepReplay]+"/{id}/turns/{seq}/promote", s.promote)
	mux.Handle("GET /audio", audio.Handler(s.blobs))
	mux.HandleFunc("GET "+ui.Routes[ui.StepExport], s.export)
	mux.HandleFunc("GET /export/dpo.jsonl", s.exportJSONL)
	mux.HandleFunc("POST /pairs/{rest...}", s.pairAction)
	return http.NewCrossOriginProtection().Handler(mux)
}

// pair is one harvested candidate with any verdict applied, ready for the
// kit. H keeps the harvested side: Review needs its audio refs and seqs.
type pair struct {
	conversationID string
	H              harvest.Pair
	ui.Pair
}

// pairs harvests every conversation, newest activity first, adds the pairs
// reviewers cut from labelled and re-run turns, and overlays the stored
// verdicts. The page reads the log each time: the journal is the source of
// truth and candidates are derived, never copied (SPEC §8).
func (s *server) pairs(ctx context.Context) ([]pair, error) {
	convs, err := s.journal.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	var out []pair
	for _, conv := range convs {
		res, err := harvest.Scan(ctx, s.journal, conv)
		if err != nil {
			return nil, fmt.Errorf("harvest %s: %w", conv, err)
		}
		reviewed, err := s.reviewersPairs(ctx, conv, res.Turns)
		if err != nil {
			return nil, err
		}
		verdicts, err := s.decisions.ForConversation(ctx, conv)
		if err != nil {
			return nil, err
		}
		for _, h := range append(res.Pairs, reviewed...) {
			d, decided := verdicts[h.ID]
			out = append(out, pair{conversationID: conv, H: h, Pair: toUIPair(h, d, decided)})
		}
	}
	return out, nil
}

// reviewersPairs cuts a pair from each turn a reviewer labelled with a fault
// and what it should have done, and from each turn whose re-run they
// promoted (SPEC §9.2). Neither is harvested: a person made them.
func (s *server) reviewersPairs(ctx context.Context, conv string, turns []harvest.Turn) ([]harvest.Pair, error) {
	annos, err := s.decisions.Annotations(ctx, conv)
	if err != nil {
		return nil, err
	}
	promos, err := s.decisions.Promotions(ctx, conv)
	if err != nil {
		return nil, err
	}
	asked, err := s.firstAsks(ctx, conv, len(promos) > 0)
	if err != nil {
		return nil, err
	}
	var out []harvest.Pair
	for _, t := range turns {
		if a := annos[t.Seq]; a.Faulted() {
			h := t.Pair(conv, harvest.SourceAnnotation, a.ShouldHave)
			h.Heard = labelled(a)
			for _, l := range a.Labels {
				h.Labels = append(h.Labels, string(l))
			}
			out = append(out, h)
		}
		if p, ok := promos[t.Seq]; ok {
			h := t.Pair(conv, harvest.SourceReplay, p.Speech)
			if r, ok := asked[t.Seq]; ok {
				rejectFirstAsk(&h, r)
			}
			h.Heard = "re-run under " + p.Versions.Model + " · " + p.Versions.Prompt
			h.ChosenVersions = p.Versions
			for _, c := range p.Calls {
				h.ChosenCalls = append(h.ChosenCalls, journal.Call{Tool: c.Tool, Args: c.Args})
			}
			// Both sides must name their configuration to train.
			h.Attributed = h.Attributed && p.Versions.Model != "" && p.Versions.Prompt != "" && p.Versions.ToolSchema != ""
			out = append(out, h)
		}
	}
	return out, nil
}

// firstAsks indexes the conversation's turns as Replay compares them, read
// only when a promotion needs them.
func (s *server) firstAsks(ctx context.Context, conv string, need bool) (map[uint64]rerun.Turn, error) {
	if !need {
		return nil, nil
	}
	events, err := s.journal.Events(ctx, conv)
	if err != nil {
		return nil, err
	}
	turns, err := rerun.Turns(events)
	if err != nil {
		return nil, fmt.Errorf("replay %s: %w", conv, err)
	}
	out := make(map[uint64]rerun.Turn, len(turns))
	for _, t := range turns {
		out[t.Seq] = t
	}
	return out, nil
}

// rejectFirstAsk makes a replay pair's rejected side the turn's first ask,
// the take the reviewer compared, not what follow-up asks added to it.
func rejectFirstAsk(h *harvest.Pair, t rerun.Turn) {
	var heard, unheard []string
	cut := false
	for _, sp := range t.Recorded.Speech {
		if sp.Text != "" {
			heard = append(heard, sp.Text)
		}
		if sp.Unheard != "" {
			// Only a truncation's tail continues the heard text verbatim.
			cut = cut || len(unheard) == 0 && sp.Text != ""
			unheard = append(unheard, sp.Unheard)
		}
	}
	h.Rejected, h.RejectedUnheard = strings.Join(heard, " "), strings.Join(unheard, " ")
	if !cut && h.Rejected != "" && h.RejectedUnheard != "" {
		h.RejectedUnheard = " " + h.RejectedUnheard
	}
	h.Calls = nil
	for _, c := range t.Recorded.Calls {
		h.Calls = append(h.Calls, journal.Call{Tool: c.Tool, Args: c.Args})
	}
	h.Versions = t.Versions
	h.Attributed = t.Recorded.Finish != "" && t.Versions.Model != "" && t.Versions.Prompt != "" && t.Versions.ToolSchema != ""
}

// toUIPair maps a harvested candidate into the kit's pair. Chosen starts as
// AsSaid so the mismatch guard, not an empty editor, is what the reviewer
// meets first: accepting the wrong-prompt side unchanged is the one mistake
// the flow exists to block (SPEC §9.1).
func toUIPair(h harvest.Pair, d curation.Decision, decided bool) ui.Pair {
	p := ui.Pair{
		ID:              h.ID,
		Source:          ui.PairSource(h.Source),
		Status:          ui.PairUnreviewed,
		Rejected:        h.Rejected,
		RejectedUnheard: h.RejectedUnheard,
		Heard:           h.Heard,
		AsSaid:          h.AsSaid,
		Chosen:          h.AsSaid,
	}
	if h.Source != harvest.SourceBargeIn {
		// A reviewer's pair already has the chosen side they wrote or promoted.
		p.Chosen = h.Chosen
	}
	if decided {
		p.Status = ui.PairStatus(d.Status)
		p.PrevStatus = ui.PairStatus(d.Prev)
		p.Chosen = d.Chosen
		p.Unfixed = d.Unfixed
		p.Reason = d.Reason
	}
	return p
}

// persist writes the pair's state back as a verdict. Unreviewed is the
// absence of a decision, so undoing to it deletes the row.
func (s *server) persist(ctx context.Context, p pair) error {
	if p.Status == ui.PairUnreviewed {
		return s.decisions.Delete(ctx, p.ID)
	}
	return s.decisions.Put(ctx, curation.Decision{
		PairID:         p.ID,
		ConversationID: p.conversationID,
		Status:         curation.Status(p.Status),
		Chosen:         p.Chosen,
		Unfixed:        p.Unfixed,
		Reason:         p.Reason,
		Prev:           curation.Status(p.PrevStatus),
		DecidedAt:      s.now(),
	})
}

func pairHref(id, status string) string {
	return ui.Routes[ui.StepCurate] + "?pair=" + url.QueryEscape(id) + "&status=" + url.QueryEscape(status)
}

type pairRow struct {
	ID, Rejected, Chosen string
	Tag, Status          ui.SigTag
	On                   bool
	Href                 string
}

type pairRowsView struct {
	Rows  []pairRow
	OOB   bool
	Empty *ui.EmptyState
}

func pairRows(pairs []pair, sel, status string, oob bool) pairRowsView {
	v := pairRowsView{OOB: oob}
	for _, p := range pairs {
		if status != "all" && string(p.Status) != status {
			continue
		}
		pa := ui.NewPairActions(p.Pair, ui.PairModeView, "", "", "", nil)
		st := pa.Status
		if p.Mismatch() && p.Status == ui.PairUnreviewed {
			st.Text += " · fix chosen"
			st.Tone = ui.ToneConv
		}
		chosen := p.Chosen
		if _, calls, _ := p.H.TextCalls(); chosen == "" {
			// A take that only calls is named by what it calls.
			chosen = strings.Join(callLines(calls), " · ")
		}
		v.Rows = append(v.Rows, pairRow{
			ID: p.ID, Rejected: p.Rejected, Chosen: chosen,
			Tag:    ui.SigTag{Text: string(p.Source), Tone: ui.TonePeople},
			Status: st, On: p.ID == sel, Href: pairHref(p.ID, status),
		})
	}
	if len(v.Rows) == 0 {
		v.Empty = &ui.EmptyState{
			Title: "Nothing in this pile.",
			Body:  "Pairs land here as the journal records barge-ins.",
		}
	}
	return v
}

func pairTabs(pairs []pair, sel, status string, oob bool) ui.Tabs {
	t := ui.Tabs{ID: "pair-tabs", Label: "Status", OOB: oob}
	for _, st := range [][2]string{
		{"all", "All"},
		{"unreviewed", "Unreviewed"},
		{"edited", "Edited"},
		{"accepted", "Accepted"},
		{"discarded", "Discarded"},
	} {
		n := 0
		for _, p := range pairs {
			if st[0] == "all" || string(p.Status) == st[0] {
				n++
			}
		}
		t.Items = append(t.Items, ui.TabItem{
			Label: st[1], Count: n, ShowCount: true, On: status == st[0],
			Href: ui.Routes[ui.StepCurate] + "?status=" + st[0] + "&pair=" + url.QueryEscape(sel),
		})
	}
	return t
}

func unreviewedCount(pairs []pair) int {
	n := 0
	for _, p := range pairs {
		if p.Status == ui.PairUnreviewed {
			n++
		}
	}
	return n
}

// find returns the pair with the exact id.
func find(pairs []pair, id string) (pair, bool) {
	for _, p := range pairs {
		if p.ID == id {
			return p, true
		}
	}
	return pair{}, false
}

// select returns the detail-pane pair for the active status tab: the asked-for
// pair when the filter admits it, else the filter's first pair, so the pane
// never shows a pair the list beside it hides.
func selectPair(pairs []pair, id, status string) (pair, bool) {
	admitted := func(p pair) bool { return status == "all" || string(p.Status) == status }
	if p, ok := find(pairs, id); ok && admitted(p) {
		return p, true
	}
	for _, p := range pairs {
		if admitted(p) {
			return p, true
		}
	}
	return pair{}, false
}

func pairCap(p pair) string {
	return "DPO pair · " + string(p.Source) + " · " + p.conversationID
}

func (s *server) pairView(p pair, mode ui.PairMode, draft string) ui.PairActions {
	pa := ui.NewPairActions(p.Pair, mode, draft, "/pairs/"+p.ID, pairCap(p), nil)
	rejected, chosen, _ := p.H.TextCalls()
	pa.RejectedCalls, pa.ChosenCalls = callLines(rejected), callLines(chosen)
	// A reviewer's pair says what made it where a barge-in names its prompt.
	switch {
	case p.Source == ui.SourceAnnotation && p.Chosen == p.H.Chosen:
		pa.Provenance = "authored by the reviewer · " + p.Heard
	case p.Source == ui.SourceReplay && p.Chosen == p.H.Chosen:
		pa.Provenance = "replay output · " + p.Heard
	case p.Source != ui.SourceBargeIn:
		pa.Provenance = "edited by you · " + p.Heard
	}
	return pa.WithDoneLinks(pairHref(p.ID, "all"), nil)
}

// callLines is each call as Curate shows it: the tool, then its arguments.
func callLines(cs []journal.Call) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Tool+" "+c.Args)
	}
	return out
}

func (s *server) curate(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pairs, err := s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status == "" {
		status = "all"
	}
	sel, selected := selectPair(pairs, q.Get("pair"), status)

	data := map[string]any{
		"Doc":    s.doc("Curate · DPO pairs"),
		"Header": ui.NewAppHeader(ui.StepCurate, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "05 · Curate", Title: "DPO pairs",
			Subtitle: "Rejected and chosen must answer the same prompt. Most harvested pairs don't until you fix the chosen side.",
		},
		"Tabs": pairTabs(pairs, sel.ID, status, false),
		"Rows": pairRows(pairs, sel.ID, status, false),
		"Pair": ui.PairActions{},
	}
	if selected {
		data["Pair"] = s.pairView(sel, ui.PairModeView, "")
	}
	s.render(w, "page-curate", data)
}

// pairAction handles POST /pairs/<id>/<action>, where the id itself carries
// a slash (conversation/cut-seq), so the action is the last segment.
func (s *server) pairAction(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rest := r.PathValue("rest")
	i := strings.LastIndex(rest, "/")
	if i <= 0 {
		http.NotFound(w, r)
		return
	}
	id, action := rest[:i], rest[i+1:]

	pairs, err := s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	p, ok := find(pairs, id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	mode, draft, err := p.Apply(action, r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Only a done flow or a save changes the pair; the other modes are views.
	if mode == ui.PairModeDone || action == "save" || action == "undo" {
		if err := s.persist(r.Context(), p); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if action == "check" {
		s.render(w, "pair-checks", s.pairView(p, mode, draft))
		return
	}
	// Re-read so the list and counts reflect the verdict just stored.
	pairs, err = s.pairs(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "pair-action-curate", map[string]any{
		"Pair": s.pairView(p, mode, draft),
		"Rows": pairRows(pairs, id, "all", true),
		"Tabs": pairTabs(pairs, id, "all", true),
	})
}

// doc is the shell every page opens with.
func (s *server) doc(title string) ui.Doc {
	return ui.Doc{Title: title, Static: "/static", Notice: s.notice}
}

func (s *server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
