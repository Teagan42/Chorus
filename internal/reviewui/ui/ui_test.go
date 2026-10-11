package ui_test

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/reviewui/ui"
	"github.com/teagan42/chorus/internal/reviewui/ui/demo"
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
		"field", "code-diff", "legend", "notice", "doc-start", "doc-end",
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
	// Ticks and bands are drawn, so a screen reader is told what each one is.
	mustContain(t, "day-lanes", h, `<span class="sr-only">wake rejected at 20:06</span>`, `<span class="sr-only">room occupied 07:00 to 09:00</span>`, `<span class="sr-only">room occupied 22:18 to 24:00</span>`)
	// A tick with somewhere to go is a link to it, and so is a lane's name.
	linked := ui.DayLanes{From: 0, To: 24, Lanes: []ui.DayLane{{
		Name: "kitchen", Href: "/conversations/device:kitchen",
		Rejects: []ui.Reject{{Hour: 14.0333, Href: "/conversations/device:kitchen#seq-7", Label: "no_speech"}},
	}}}
	h = render(t, "day-lanes", linked)
	mustContain(t, "linked day-lanes", h,
		`<a class="day-lanes__head" href="/conversations/device:kitchen"><span class="day-lanes__name">kitchen</span>`,
		`<a class="day-lanes__reject" href="/conversations/device:kitchen#seq-7" aria-label="wake rejected at 14:02 · no_speech" title="wake rejected at 14:02 · no_speech" style="left: 58.472%"></a>`)

	h = render(t, "clip-grid", demo.Clips(map[string]string{"w2": "neg"}, func(id, m string) string { return "/clips/" + id + "/" + m }))
	mustContain(t, "clip-grid", h, "2 of 3 passed", "auto negative", `class="clip-tile is-reviewed"`, `role="region" aria-label="Clips"`)

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

// verifies SPEC §9.2
func TestTheShellServesHtmxFromItsOwnStatic(t *testing.T) {
	h := render(t, "doc-start", ui.Doc{Title: "Curate", Static: "/static"})
	if !strings.Contains(h, `src="/static/js/htmx.min.js"`) {
		t.Error("htmx is not served from the embedded bundle")
	}
	if strings.Contains(h, "unpkg.com") {
		t.Error("the shell still reaches for unpkg; an offline review box gets inert controls")
	}
	if f, err := ui.StaticFS().Open("js/htmx.min.js"); err != nil {
		t.Errorf("the vendored htmx is not in the bundle: %v", err)
	} else {
		_ = f.Close()
	}
}

// A row with nowhere to go must not be a link to the page it is on.
func TestListRowWithoutAHrefIsNotALink(t *testing.T) {
	h := render(t, "list-row", ui.ListRow{Title: "Is the garage door closed?"})
	if strings.Contains(h, "<a") || strings.Contains(h, "href") {
		t.Errorf("hrefless row rendered as a link: %s", h)
	}
	mustContain(t, "hrefless row", h, `class="list__row"`, "Is the garage door closed?")

	h = render(t, "list-row", ui.ListRow{Href: "/review?pair=x", Title: "linked"})
	mustContain(t, "linked row", h, `<a class="list__row" href="/review?pair=x"`)
}

// The hosted demo says what it is above every screen; the household's own
// review box says nothing.
func TestTheShellCarriesANoticeOnlyWhenGivenOne(t *testing.T) {
	h := render(t, "doc-start", ui.Doc{Title: "Browse", Static: "/static", Notice: &ui.Notice{
		Label: "Demo", Text: "Teagan, Alice and Alan's Thursday. Verdicts reset when you reload.",
		LinkText: "About Chorus", Href: "https://teagan42.github.io/Chorus/",
	}})
	mustContain(t, "notice", h, `<aside class="notice tone-conv" role="note" aria-label="Demo">`,
		"Alice and Alan&#39;s Thursday", `href="https://teagan42.github.io/Chorus/">About Chorus ↗</a>`)
	if h := render(t, "doc-start", ui.Doc{Title: "Browse", Static: "/static"}); strings.Contains(h, "notice") {
		t.Error("a review box with no notice still renders the strip")
	}
}

// A page about a household's recordings tells no third party it was opened:
// the fonts are in the binary beside htmx, with their licences, and the
// shell names nothing on Google.
//
// verifies SPEC §1
func TestTheFontsComeFromTheBinary(t *testing.T) {
	h := render(t, "doc-start", ui.Doc{Title: "Browse", Static: "/static"})
	for _, leak := range []string{"googleapis", "gstatic"} {
		if strings.Contains(h, leak) {
			t.Errorf("the shell still reaches for %s", leak)
		}
	}
	for _, f := range []string{
		"css/fonts.css",
		"fonts/Outfit-Variable.ttf", "fonts/OFL-Outfit.txt",
		"fonts/JetBrainsMono-Regular.woff2", "fonts/JetBrainsMono-Medium.woff2", "fonts/OFL-JetBrainsMono.txt",
	} {
		if _, err := fs.Stat(ui.StaticFS(), f); err != nil {
			t.Errorf("%s is not in the bundle: %v", f, err)
		}
	}
	css, err := fs.ReadFile(ui.StaticFS(), "css/chorus.css")
	if err != nil || !strings.Contains(string(css), `@import "fonts.css"`) {
		t.Errorf("chorus.css does not import fonts.css (%v):\n%s", err, css)
	}
	faces, _ := fs.ReadFile(ui.StaticFS(), "css/fonts.css")
	for _, want := range []string{"font-family: 'Outfit'", "font-family: 'JetBrains Mono'", "../fonts/Outfit-Variable.ttf", "../fonts/JetBrainsMono-Regular.woff2", "../fonts/JetBrainsMono-Medium.woff2"} {
		if !strings.Contains(string(faces), want) {
			t.Errorf("fonts.css lacks %q", want)
		}
	}
	for file, typ := range map[string]string{"fonts/JetBrainsMono-Regular.woff2": "font/woff2", "fonts/Outfit-Variable.ttf": "font/ttf"} {
		w := httptest.NewRecorder()
		ui.Static().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+file, nil))
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != typ {
			t.Errorf("GET %s = %d %s, want 200 %s", file, w.Code, w.Header().Get("Content-Type"), typ)
		}
	}
}

// A failed post is said out loud: the shell loads the script that listens
// for htmx's errors and carries the region it writes into.
//
// verifies SPEC §9.2
func TestTheShellSaysWhenARequestFails(t *testing.T) {
	h := render(t, "doc-start", ui.Doc{Title: "Browse", Static: "/static"})
	for _, want := range []string{`src="/static/js/chorus.js"`, `id="alerts"`, `aria-live="assertive"`} {
		if !strings.Contains(h, want) {
			t.Errorf("the shell lacks %s", want)
		}
	}
	js, err := fs.ReadFile(ui.StaticFS(), "js/chorus.js")
	if err != nil {
		t.Fatalf("chorus.js is not in the bundle: %v", err)
	}
	for _, want := range []string{"htmx:responseError", "htmx:sendError", `getElementById("alerts")`, "alert__title", "alert__body"} {
		if !strings.Contains(string(js), want) {
			t.Errorf("chorus.js lacks %s", want)
		}
	}
}
