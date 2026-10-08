package ui_test

import (
	"bytes"
	"io/fs"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/reviewui/ui"
	"github.com/teaganglenn/chorus/internal/reviewui/ui/demo"
)

func render(t *testing.T, name string, data any) string {
	t.Helper()
	tpl := ui.MustTemplates()
	var b bytes.Buffer
	if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return b.String()
}

func mustContain(t *testing.T, name, html string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(html, w) {
			t.Errorf("%s: missing %q", name, w)
		}
	}
}

// Every component has a template and a stylesheet, and chorus.css imports it.
func TestEveryComponentHasTemplateAndCSS(t *testing.T) {
	static := ui.StaticFS()
	bundle, err := fs.ReadFile(static, "css/chorus.css")
	if err != nil {
		t.Fatal(err)
	}
	css, _ := fs.Glob(static, "css/components/*.css")
	if len(css) == 0 {
		t.Fatal("no component css")
	}
	for _, c := range css {
		if !strings.Contains(string(bundle), "components/"+path.Base(c)) {
			t.Errorf("chorus.css does not import %s", c)
		}
	}
	tpl := ui.MustTemplates()
	for _, name := range []string{
		"app-header", "page-head", "button", "segmented", "tabs", "chip", "chip-group", "switch",
		"sig-tag", "metric-strip", "transport", "timeline", "event-pins", "conv-thumb", "list", "list-row", "day-lanes", "option-cards", "data-table",
		"clip-tile", "clip-grid", "inspector", "pair-actions", "pair-checks", "checks", "alert", "done-card", "empty-state",
		"field", "code-diff", "legend", "doc-start", "doc-end",
	} {
		if tpl.Lookup(name) == nil {
			t.Errorf("template %q not defined", name)
		}
	}
}

func TestRenderComponents(t *testing.T) {
	insp := func(id string) string { return "/review/events/" + id }

	h := render(t, "app-header", demo.Header(ui.StepReview))
	mustContain(t, "app-header", h, `aria-current="page"`, ">Review<", `class="app-header__count">31<`)

	h = render(t, "timeline", demo.ReviewTimeline("e35", insp))
	mustContain(t, "timeline", h, "timeline__overlay is-cut", "is-unheard", `hx-get="/review/events/e41"`, `aria-pressed="true"`)

	h = render(t, "timeline", demo.MovedTimeline())
	mustContain(t, "moved timeline", h, "timeline__gap", "held 1:32", "Conversation · Alan")

	h = render(t, "list", demo.QueueList())
	mustContain(t, "list", h, "sig-tag tone-people", "conv-thumb__seg is-cut")

	h = render(t, "list", demo.FilterList(demo.QueueList(), "zzz"))
	mustContain(t, "empty list", h, "Nothing matches.")

	h = render(t, "day-lanes", demo.Household())
	mustContain(t, "day-lanes", h, "day-lanes__migration", "Alan moved rooms", "legend__swatch--flag tone-people")

	h = render(t, "clip-grid", demo.Clips(map[string]string{"w2": "neg"}, func(id, m string) string { return "/clips/" + id + "/" + m }))
	mustContain(t, "clip-grid", h, "2 of 3 passed", "auto negative", `class="clip-tile is-reviewed"`)

	h = render(t, "metric-strip", demo.MovedHandoff())
	mustContain(t, "metric-strip", h, "metric__meter", "width: 76.000%")

	h = render(t, "code-diff", demo.PromptDiff())
	mustContain(t, "code-diff", h, "is-del", "is-add")

	h = render(t, "page-head", ui.PageHead{
		Eyebrow: "05 · Curate", Title: "DPO pairs",
		Switch: &ui.Switch{Label: "Curate", Items: []ui.SwitchItem{{Label: "DPO pairs", Href: "/p", On: true}, {Label: "Hard negatives", Href: "/n"}}},
		Stats:  []ui.Metric{{Label: "Exportable", Value: "27"}}, Actions: []ui.Button{{Label: "Export accepted", Href: "/export"}},
	})
	mustContain(t, "page-head", h, "switch__item is-on", "page-head__stat-value", `<a class="btn" href="/export">`)

	h = render(t, "transport", ui.Transport{Timecode: ui.Timecode(5.45), Channels: &ui.Segmented{Label: "Channel", Items: []ui.SegItem{{Label: "AEC’d", On: true}, {Label: "Raw"}}}})
	mustContain(t, "transport", h, "00:05.450", `aria-label="Play"`, "segmented__item is-on")

	h = render(t, "field", ui.Field{Kind: ui.FieldSelect, ID: "p", Label: "Person", Options: []ui.Option{{Value: "Anyone"}, {Value: "Alan", Selected: true}}})
	mustContain(t, "field", h, `<option value="Alan" selected>`)

	h = render(t, "button", ui.Button{Label: "Next", Hx: ui.Hx{Post: "/a?x=1&y=2", Vals: `{"reason":"Duplicate"}`}})
	mustContain(t, "button", h, `hx-post="/a?x=1&amp;y=2"`, `hx-vals="{&#34;reason&#34;:&#34;Duplicate&#34;}"`)
}

func TestPairFlow(t *testing.T) {
	p := demo.Pairs()["p41"]
	form := url.Values{}

	if !p.Mismatch() {
		t.Fatal("harvested barge-in pair should start mismatched")
	}
	mode, _, _ := p.Apply("accept", form)
	if mode != ui.PairModeGuard || p.Status != ui.PairUnreviewed {
		t.Fatalf("accept on mismatch: got mode %s status %s, want guard/unreviewed", mode, p.Status)
	}
	h := render(t, "pair-actions", ui.NewPairActions(*p, mode, "", "/pairs/p41", "DPO PAIR", nil))
	mustContain(t, "guard", h, "replies to the wrong prompt", `hx-post="/pairs/p41/accept-anyway"`)

	mode, draft, _ := p.Apply("draft", form)
	if mode != ui.PairModeEdit || draft != p.Suggestion {
		t.Fatalf("draft: got %s %q", mode, draft)
	}
	pa := ui.NewPairActions(*p, mode, draft, "/pairs/p41", "DPO PAIR", nil)
	for _, c := range pa.Checks {
		if !c.On {
			t.Errorf("check %q should pass for the suggestion", c.Text)
		}
	}
	h = render(t, "pair-actions", pa)
	mustContain(t, "edit", h, `name="chosen"`, `hx-post="/pairs/p41/check"`, `id="pair-p41-checks"`)

	form.Set("chosen", draft)
	_, _, _ = p.Apply("save", form)
	if p.Mismatch() || p.Status != ui.PairEdited {
		t.Fatalf("save: mismatch=%v status=%s", p.Mismatch(), p.Status)
	}
	mode, _, _ = p.Apply("accept", form)
	if mode != ui.PairModeDone || !p.Exportable() {
		t.Fatalf("accept fixed pair: mode %s exportable %v", mode, p.Exportable())
	}
	_, _, _ = p.Apply("undo", form)
	if p.Status != ui.PairEdited {
		t.Fatalf("undo: status %s, want edited", p.Status)
	}

	q := demo.Pairs()["p38"]
	_, _, _ = q.Apply("accept-anyway", form)
	if q.Exportable() || !q.Unfixed {
		t.Fatal("accept-anyway must not be exportable")
	}
	h = render(t, "pair-actions", ui.NewPairActions(*q, ui.PairModeDone, "", "/pairs/p38", "DPO PAIR", nil))
	mustContain(t, "flagged", h, "Accepted as-is · flagged", "accepted · mismatch", `hx-post="/pairs/p38/edit"`)

	r := demo.Pairs()["p35"]
	if r.Mismatch() {
		t.Fatal("annotation pairs are never mismatched")
	}
	form.Set("reason", "Duplicate")
	_, _, _ = r.Apply("discard", form)
	_, _, _ = r.Apply("reason", form)
	if r.Status != ui.PairDiscarded || r.Reason != "Duplicate" {
		t.Fatalf("discard: %s %q", r.Status, r.Reason)
	}
	if _, _, err := r.Apply("explode", form); err == nil {
		t.Fatal("unknown action should error")
	}
}

func TestAxes(t *testing.T) {
	g := demo.MovedAxis
	if p := g.Pos(12); p < 51.9 || p > 52.1 {
		t.Errorf("end of first span at %.2f, want 52", p)
	}
	if p := g.Pos(100); p < 59.9 || p > 60.1 {
		t.Errorf("start of second span at %.2f, want 60", p)
	}
	if gaps := g.Gaps(); len(gaps) != 1 || gaps[0].Label != "+1:28" {
		t.Errorf("gaps = %+v", gaps)
	}
	pins := ui.AssignRows([]ui.Pin{{Label: "Teagan", Pos: 28.18}, {Label: "→ light", Pos: 31.6}, {Label: "→ calendar", Pos: 31.7}}, 1240)
	if pins[0].Row == pins[1].Row || pins[1].Row == pins[2].Row {
		t.Errorf("overlapping pins share a row: %+v", pins)
	}
}

// verifies SPEC §9.1
func TestSavingTheSameMismatchedTextKeepsTheFlag(t *testing.T) {
	form := url.Values{}
	p := demo.Pairs()["p41"]
	_, _, _ = p.Apply("accept-anyway", form)
	if !p.Unfixed {
		t.Fatal("accept-anyway did not flag the pair")
	}

	// Saving the mismatched text unchanged fixes nothing, so the flag stays.
	form.Set("chosen", p.AsSaid)
	_, _, _ = p.Apply("save", form)
	if !p.Unfixed {
		t.Error("a no-op save cleared the mismatch flag")
	}
	if p.Exportable() {
		t.Error("a still-mismatched pair became exportable")
	}

	// A real fix clears it.
	form.Set("chosen", "Dentist at nine, and the lights are off.")
	_, _, _ = p.Apply("save", form)
	if p.Unfixed {
		t.Error("fixing the chosen side left the flag set")
	}
}

// verifies SPEC §9.2
func TestPairIDsWithSlashesRenderSelectorSafeTargets(t *testing.T) {
	p := *demo.Pairs()["p41"]
	p.ID = "conv-1/5"
	pa := ui.NewPairActions(p, ui.PairModeEdit, p.Chosen, "/pairs/conv-1/5", "DPO PAIR", nil)
	h := render(t, "pair-actions", pa)
	for _, want := range []string{
		`id="pair-conv-1-5-checks"`, `hx-target="#pair-conv-1-5-checks"`, `id="pair-conv-1-5-chosen"`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("edit mode is missing %q", want)
		}
	}
	if strings.Contains(h, `id="pair-conv-1/5`) {
		t.Error("a raw slash reached a DOM id, which no CSS selector can address")
	}
}
