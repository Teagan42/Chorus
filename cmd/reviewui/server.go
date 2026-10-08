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

	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
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
	now       func() time.Time

	// One reviewer at a time is the household reality; the lock keeps a
	// read-modify-write on a pair from racing itself.
	mu sync.Mutex
}

func newServer(j Journal, d curation.Store, now func() time.Time) *server {
	return &server{
		tpl:       template.Must(ui.MustTemplates().ParseFS(pagesFS, "pages/*.tmpl")),
		journal:   j,
		decisions: d,
		now:       now,
	}
}

func (s *server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", ui.Static()))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, ui.Routes[ui.StepCurate], http.StatusSeeOther)
	})
	mux.HandleFunc("GET "+ui.Routes[ui.StepCurate], s.curate)
	mux.HandleFunc("POST /pairs/{rest...}", s.pairAction)
	return mux
}

// pair is one harvested candidate with any verdict applied, ready for the kit.
type pair struct {
	conversationID string
	ui.Pair
}

// pairs harvests every conversation, newest activity first, and overlays the
// stored verdicts. The page reads the log each time: the journal is the
// source of truth and candidates are derived, never copied (SPEC §8).
func (s *server) pairs(ctx context.Context) ([]pair, error) {
	convs, err := s.journal.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	var out []pair
	for _, conv := range convs {
		harvested, err := harvest.Harvest(ctx, s.journal, conv)
		if err != nil {
			return nil, fmt.Errorf("harvest %s: %w", conv, err)
		}
		verdicts, err := s.decisions.ForConversation(ctx, conv)
		if err != nil {
			return nil, err
		}
		for _, h := range harvested {
			d, decided := verdicts[h.ID]
			out = append(out, pair{conversationID: conv, Pair: toUIPair(h, d, decided)})
		}
	}
	return out, nil
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
		v.Rows = append(v.Rows, pairRow{
			ID: p.ID, Rejected: p.Rejected, Chosen: p.Chosen,
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

// find returns the selected pair, falling back to the first.
func find(pairs []pair, id string) (pair, bool) {
	for _, p := range pairs {
		if p.ID == id {
			return p, true
		}
	}
	if len(pairs) > 0 {
		return pairs[0], true
	}
	return pair{}, false
}

func pairCap(p pair) string {
	return "DPO pair · " + string(p.Source) + " · " + p.conversationID
}

func (s *server) pairView(p pair, mode ui.PairMode, draft string) ui.PairActions {
	pa := ui.NewPairActions(p.Pair, mode, draft, "/pairs/"+p.ID, pairCap(p), nil)
	return pa.WithDoneLinks(pairHref(p.ID, "all"), nil)
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
	sel, selected := find(pairs, q.Get("pair"))

	data := map[string]any{
		"Doc":    ui.Doc{Title: "Curate · DPO pairs", Static: "/static"},
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
	if !ok || p.ID != id {
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

func (s *server) render(w http.ResponseWriter, name string, data any) {
	if err := s.tpl.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
