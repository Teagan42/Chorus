//go:build e2e

// Journeys: a reviewer working through the household's Thursday
// (household_test.go) the way the screens are meant to be used, one screen
// handing off to the next, with what each one stores checked on the others.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/reviewui/household"
	sess "github.com/teagan42/chorus/internal/session"
)

// ------------------------------------------------------------------ helpers

// expect lets one error response through: a test that asks for a page that
// should not exist still fails on any other.
func (p *page) expect(status int, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.allowed == nil {
		p.allowed = map[string]int{}
	}
	p.allowed[p.base+path] = status
}

func (p *page) count(sel string) int {
	p.t.Helper()
	var n int
	p.eval(fmt.Sprintf(`document.querySelectorAll(%q).length`, sel), &n)
	return n
}

func (p *page) has(sel string) bool { p.t.Helper(); return p.count(sel) > 0 }

// counts reads a tab strip as label → count.
func (p *page) counts(tabs string) map[string]int {
	p.t.Helper()
	out := map[string]int{}
	p.eval(fmt.Sprintf(`Object.fromEntries([...document.querySelectorAll(%q)].map(a => [
		a.childNodes[0].textContent.trim(), +((a.querySelector(".tabs__count") || {}).textContent || 0)]))`, tabs+" .tabs__tab"), &out)
	return out
}

// metrics reads a metric strip as label → value.
func (p *page) metrics(strip string) map[string]string {
	p.t.Helper()
	out := map[string]string{}
	p.eval(fmt.Sprintf(`Object.fromEntries([...document.querySelectorAll(%q)].map(m => [
		m.querySelector(".cap").textContent.trim(), m.querySelector(".metric__value").textContent.trim()]))`, strip+" .metric"), &out)
	return out
}

// badge is the unreviewed count the header carries on Triage, 0 when hidden.
func (p *page) badge() int {
	p.t.Helper()
	var n int
	p.eval(`+((document.querySelector('header nav a[href$="/queue"] .app-header__count') || {}).textContent || 0)`, &n)
	return n
}

// target is the element the URL's fragment names, and whether it is on screen.
func (p *page) target() (id string, visible bool) {
	p.t.Helper()
	var v struct {
		ID      string
		Visible bool
	}
	p.eval(`(() => {
		const e = document.querySelector(":target");
		if (!e) return {ID: "", Visible: false};
		const r = e.getBoundingClientRect();
		return {ID: e.id, Visible: r.top >= 0 && r.bottom <= window.innerHeight};
	})()`, &v)
	return v.ID, v.Visible
}

// durations loads every <audio> under sel and returns how long each lasts.
func (p *page) durations(sel string) []float64 {
	p.t.Helper()
	var d []float64
	p.eval(fmt.Sprintf(`Promise.all([...document.querySelectorAll(%q)].map(a => new Promise((ok, no) => {
		a.addEventListener("loadedmetadata", () => ok(a.duration), {once: true});
		a.addEventListener("error", () => no(new Error("cannot decode " + a.src)), {once: true});
		a.preload = "metadata"; a.load();
	})))`, sel+" audio"), &d)
	return d
}

func closeTo(t *testing.T, what string, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: %d clips %v, want %d %v", what, len(got), got, len(want), want)
		return
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > 0.01 {
			t.Errorf("%s: clip %d lasts %.3fs, want %.2fs", what, i, got[i], want[i])
		}
	}
}

// typeInto replaces a field's text by typing, so the input events htmx
// listens for actually fire.
func (p *page) typeInto(sel, text string) {
	p.t.Helper()
	p.run(
		chromedp.WaitVisible(sel, chromedp.ByQuery),
		chromedp.Evaluate(fmt.Sprintf(`document.querySelector(%q).value = ""`, sel), nil),
		chromedp.SendKeys(sel, text, chromedp.ByQuery),
	)
}

// press clicks a pair-actions control by the action it posts.
func (p *page) press(action string) {
	p.t.Helper()
	p.click(fmt.Sprintf(`.pair-actions [hx-post$="/%s"]`, action))
}

func curateRow(id string) string {
	return fmt.Sprintf(`#pair-rows a[href*="pair=%s&"]`, url.QueryEscape(id))
}

// rowStatus is the status tag on a pair's row in the Curate list.
func (p *page) rowStatus(id string) string {
	p.t.Helper()
	var s string
	p.eval(fmt.Sprintf(`(() => { const r = document.querySelector(%q); return r ? r.querySelectorAll(".sig-tag")[1].textContent.trim() : "" })()`, curateRow(id)), &s)
	return s
}

// checked lists the editor's checks that pass.
func (p *page) checked() []string {
	p.t.Helper()
	var on []string
	p.eval(`[...document.querySelectorAll(".pair-actions .checks__item.is-on")].map(li => li.childNodes[1].textContent.trim())`, &on)
	return on
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}

// dpoRow is the part of a downloaded JSONL row the journeys check.
type dpoRow struct {
	Prompt, Chosen, Rejected []struct{ Role, Content string }
	Meta                     struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversation_id"`
		Curated        bool   `json:"curated"`
		Attributed     bool   `json:"attributed"`
		Heard          string `json:"heard"`
	}
}

// ----------------------------------------------------------------- journeys

// Morning coffee: Triage says three barge-ins happened. The reviewer opens
// Teagan's weather cut from the queue, hears where it was cut, refuses the
// as-said answer, writes the one that answers the forecast question, and
// accepts it. Curate, the header and Review all agree afterwards.
//
// verifies SPEC §9.1
func TestE2EJourneyTriageToReviewFixesAndAcceptsTheWeatherCut(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/queue")
	if got := p.badge(); got != 3 {
		t.Errorf("header badge = %d, want 3 unreviewed pairs", got)
	}
	want := map[string]int{"All": 6, "Barge-in pairs": 3, "Repeated": 1, "Slow": 0, "Failures": 1, "Speaker flips": 1}
	if got := p.counts("#queue-tabs"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("queue tabs = %v, want %v", got, want)
	}
	p.follow(`#queue-tabs a[href$="tab=barge-in"]`)
	if n := p.count("#queue a.list__row"); n != 3 {
		t.Errorf("barge-in tab lists %d rows, want 3", n)
	}
	p.shot("journey-triage-bargeins")

	p.follow(fmt.Sprintf(`#queue a[href="%s"]`, reviewHref(pairWeather)))
	p.waitText("h1", "what's the weather today")
	p.waitText(".inspector", "correction · teagan: do I need an umbrella")
	// Heard stops at the 24000 frames the DAC played; the tail is the rest
	// of the 4 s forecast.
	closeTo(t, "weather clips", p.durations(`[aria-label="Clips"]`), []float64{1.5, 2.5, 0.6, 1.1, 1.5})

	// The as-said answer replies to the umbrella, not the forecast.
	p.press("accept")
	p.waitText(".pair-actions", "This chosen answer replies to the wrong prompt.")
	p.shot("journey-review-guard")
	p.press("edit") // Fix it first
	p.waitText(".pair-actions", "Save chosen")
	if on := p.checked(); contains(on, "answers the shared prompt") {
		t.Errorf("checks %v pass the as-said answer as answering the prompt", on)
	}
	p.typeInto(".pair-actions textarea", fixWeather)
	// The checks re-render from the server as the reviewer types.
	var live bool
	if err := chromedp.Run(p.ctx, chromedp.Poll(`[...document.querySelectorAll(".pair-actions .checks__item.is-on")].some(li => li.textContent.includes("answers the shared prompt"))`,
		&live, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Errorf("typing the fix never passed \"answers the shared prompt\"; passing: %v", p.checked())
	}
	if on := p.checked(); !contains(on, "short enough to hear") || !contains(on, "honours the correction") {
		t.Errorf("after typing the fix the checks passing are %v", on)
	}
	p.shot("journey-review-editing")
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", "edited by you · answers the shared prompt")

	p.press("accept")
	p.waitText(".done-card", "Accepted · "+pairWeather)
	p.shot("journey-review-accepted")
	if d, ok, _ := decisions.Get(context.Background(), pairWeather); !ok || d.Status != "accepted" || d.Chosen != fixWeather || d.Unfixed {
		t.Errorf("stored verdict = %+v (ok %v), want accepted with the fix", d, ok)
	}

	// The done card hands off to Curate with the pair selected.
	p.follow(`.done-card a[href^="/curate/pairs"]`)
	p.waitText(".pair-actions", fixWeather)
	if got := p.counts("#pair-tabs"); got["Accepted"] != 1 || got["Unreviewed"] != 2 {
		t.Errorf("curate tabs = %v, want 1 accepted and 2 unreviewed", got)
	}
	if got := p.rowStatus(pairWeather); got != "accepted" {
		t.Errorf("weather row says %q, want accepted", got)
	}
	if got := p.badge(); got != 2 {
		t.Errorf("header badge = %d after one verdict, want 2", got)
	}

	// Review, reloaded, shows the verdict rather than a fresh pair.
	p.visit(reviewHref(pairWeather))
	p.waitText(".pair-actions", "accepted")
	p.waitText(".pair-actions", fixWeather)
}

// Evening: the reviewer clears Curate in one sitting. Alice's Zeppelin cut is
// accepted in a hurry, flagged, then fixed from the done card; Alan's jazz cut
// was the TV, so it is discarded as noise. Export then holds exactly what
// Curate decided, and the download is the file a trainer would get.
//
// verifies SPEC §9.1
func TestE2EJourneyCurateEveryPairThenDownloadTheDataset(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/curate/pairs")
	// Newest activity first: the jazz cut heads the list.
	var order []string
	p.eval(`[...document.querySelectorAll("#pair-rows a")].map(a => new URL(a.href).searchParams.get("pair"))`, &order)
	if fmt.Sprint(order) != fmt.Sprint([]string{pairJazz, pairZeppel, pairWeather}) {
		t.Errorf("curate lists %v, want jazz, zeppelin, weather", order)
	}
	for _, id := range order {
		if got := p.rowStatus(id); got != "unreviewed · fix chosen" {
			t.Errorf("%s row says %q, want the mismatch flagged before any click", id, got)
		}
	}
	p.shot("journey-curate-start")

	// Zeppelin: accept anyway, then fix it from the done card.
	p.follow(curateRow(pairZeppel))
	p.waitText(".pair-actions", "I found three")
	p.press("accept")
	p.press("accept-anyway")
	p.waitText(".done-card", "Accepted as-is · flagged")
	p.waitText("#pair-rows", "accepted · mismatch")
	if got := p.counts("#pair-tabs"); got["Accepted"] != 1 {
		t.Errorf("tabs after accept-anyway = %v, want 1 accepted", got)
	}
	p.click(`.done-card [hx-post$="/edit"]`) // Fix chosen
	p.typeInto(".pair-actions textarea", fixZeppel)
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", fixZeppel)
	if got := p.rowStatus(pairZeppel); got != "accepted" {
		t.Errorf("zeppelin row says %q after the fix, want accepted (no longer flagged)", got)
	}

	// Jazz: the barge-in was the TV.
	p.follow(curateRow(pairJazz))
	p.waitText(".pair-actions", "Playing Late Night Jazz")
	p.press("discard")
	p.waitText(".pair-actions", "Why discard it?")
	p.shot("journey-curate-discard")
	p.click(`.pair-actions button[hx-vals*="Barge-in was noise"]`)
	p.waitText(".done-card", "Discarded · Barge-in was noise")

	// Weather: fixed and accepted straight from Curate.
	p.follow(curateRow(pairWeather))
	p.press("edit")
	p.typeInto(".pair-actions textarea", fixWeather)
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", "edited")
	p.press("accept")
	p.waitText(".done-card", "Accepted · "+pairWeather)

	want := map[string]int{"All": 3, "Unreviewed": 0, "Edited": 0, "Accepted": 2, "Discarded": 1}
	if got := p.counts("#pair-tabs"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tabs after the sitting = %v, want %v", got, want)
	}
	p.follow(`#pair-tabs a[href*="status=discarded"]`)
	if n := p.count("#pair-rows a"); n != 1 || !strings.Contains(p.text("#pair-rows"), "Playing Late Night Jazz") {
		t.Errorf("discarded tab lists %d rows: %q", n, p.text("#pair-rows"))
	}
	p.shot("journey-curate-done")

	p.follow(`header nav a[href="/export"]`)
	if got := p.badge(); got != 0 {
		t.Errorf("header badge = %d with every pair reviewed, want none", got)
	}
	stats := p.metrics(`[aria-label="Piles"]`)
	for label, want := range map[string]string{"Exportable": "2", "Unreviewed": "0", "Discarded": "1", "Held · unfixed": "0", "Held · unattributed": "0"} {
		if stats[label] != want {
			t.Errorf("export %s = %q, want %s (all: %v)", label, stats[label], want, stats)
		}
	}
	p.waitText(`a[href="/export/dpo.jsonl"]`, "Download 2 rows (JSONL)")
	p.waitText("body", fixWeather)
	p.shot("journey-export")

	var dl struct {
		Type, Disposition, Body string
	}
	p.eval(`fetch(document.querySelector('a[href="/export/dpo.jsonl"]').href).then(async r => ({
		Type: r.headers.get("content-type"), Disposition: r.headers.get("content-disposition"), Body: await r.text()}))`, &dl)
	if dl.Type != "application/jsonl" || !strings.Contains(dl.Disposition, `filename="chorus-dpo.jsonl"`) {
		t.Errorf("download is %q / %q, want a JSONL attachment", dl.Type, dl.Disposition)
	}
	var rows []dpoRow
	for _, line := range strings.Split(strings.TrimSpace(dl.Body), "\n") {
		var r dpoRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("download line %q: %v", line, err)
		}
		rows = append(rows, r)
	}
	if len(rows) != 2 {
		t.Fatalf("download has %d rows, want 2", len(rows))
	}
	for i, w := range []struct{ id, chosen, rejected, prompt, heard string }{
		{pairZeppel, fixZeppel, "I found three albums by that artist: Led Zeppelin, Led Zeppelin II and Led Zeppelin IV.", "play something by zeppelin", "just the first one"},
		{pairWeather, fixWeather, "Today will be cloudy in the morning, with rain from three and a high of fourteen.", "what's the weather today", "do I need an umbrella"},
	} {
		r := rows[i]
		if r.Meta.ID != w.id || !r.Meta.Curated || !r.Meta.Attributed || r.Meta.Heard != w.heard {
			t.Errorf("row %d meta = %+v, want %s curated and attributed", i, r.Meta, w.id)
		}
		if len(r.Chosen) != 1 || r.Chosen[0].Content != w.chosen {
			t.Errorf("row %d chosen = %+v, want the reviewer's fix", i, r.Chosen)
		}
		if len(r.Rejected) != 1 || r.Rejected[0].Content != w.rejected {
			t.Errorf("row %d rejected = %+v, want heard + unheard", i, r.Rejected)
		}
		if n := len(r.Prompt); n == 0 || r.Prompt[n-1].Content != w.prompt {
			t.Errorf("row %d prompt = %+v, want it to end on %q", i, r.Prompt, w.prompt)
		}
	}
}

// What must not reach a trainer: a pair accepted while it still answers the
// correction, and one no model can be credited with. Export holds both back
// and says why, until the first is fixed.
//
// verifies SPEC §9.1
func TestE2EJourneyExportHoldsBackUnfixedAndUnattributedPairs(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/curate/pairs?pair=" + url.QueryEscape(pairZeppel))
	p.press("accept")
	p.press("accept-anyway")
	p.waitText(".done-card", "Accepted as-is · flagged")

	p.follow(curateRow(pairJazz))
	p.waitText(".pair-actions", "Playing Late Night Jazz")
	p.press("edit")
	p.typeInto(".pair-actions textarea", "I found three jazz playlists. Playing Late Night Jazz, the quietest of them.")
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", "edited by you")
	p.press("accept")
	p.waitText(".done-card", "Accepted · "+pairJazz)

	p.visit("/export")
	stats := p.metrics(`[aria-label="Piles"]`)
	for label, want := range map[string]string{"Exportable": "0", "Held · unfixed": "1", "Held · unattributed": "1", "Unreviewed": "1"} {
		if stats[label] != want {
			t.Errorf("export %s = %q, want %s (all: %v)", label, stats[label], want, stats)
		}
	}
	p.waitText("body", "Nothing to export yet.")
	if p.has(`a[href="/export/dpo.jsonl"]`) {
		t.Error("export links a download with nothing exportable")
	}
	p.shot("journey-export-held")

	// Fixing the Zeppelin pair from the Accepted pile releases it.
	p.visit("/curate/pairs?status=accepted")
	p.follow(curateRow(pairZeppel))
	p.waitText(".pair-actions", "accepted · mismatch")
	p.press("edit")
	p.typeInto(".pair-actions textarea", fixZeppel)
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", fixZeppel)

	p.visit("/export")
	stats = p.metrics(`[aria-label="Piles"]`)
	if stats["Exportable"] != "1" || stats["Held · unfixed"] != "0" || stats["Held · unattributed"] != "1" {
		t.Errorf("after the fix export piles are %v, want 1 exportable and the jazz pair still held", stats)
	}
	var lines int
	p.eval(`fetch("/export/dpo.jsonl").then(r => r.text()).then(t => t.trim().split("\n").filter(Boolean).length)`, &lines)
	if lines != 1 {
		t.Errorf("download has %d rows, want only the fixed Zeppelin pair", lines)
	}
}

// Second thoughts: every way out of a decision leaves the store as it was.
//
// verifies SPEC §9.1
func TestE2EJourneyCancelAndUndoLeaveNoVerdict(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)
	none := func(when string) {
		t.Helper()
		for _, id := range []string{pairWeather, pairZeppel, pairJazz} {
			if d, ok, _ := decisions.Get(context.Background(), id); ok {
				t.Errorf("%s: %s has a stored verdict %+v", when, id, d)
			}
		}
	}

	p.visit("/curate/pairs?pair=" + url.QueryEscape(pairWeather))
	p.press("accept")
	p.waitText(".pair-actions", "replies to the wrong prompt")
	p.press("cancel")
	p.waitText(".pair-actions", "Accept pair")
	none("guard cancelled")

	p.press("edit")
	p.typeInto(".pair-actions textarea", "Take an umbrella.")
	p.press("cancel")
	p.waitText(".pair-actions", "Yes, rain from three.")
	none("edit cancelled")
	p.visit("/curate/pairs?pair=" + url.QueryEscape(pairWeather))
	if strings.Contains(p.text(".pair-actions"), "Take an umbrella.") {
		t.Error("a cancelled edit survived a reload")
	}

	p.press("discard")
	p.click(`.pair-actions button[hx-vals*="Duplicate"]`)
	p.waitText(".done-card", "Discarded · Duplicate")
	if d, ok, _ := decisions.Get(context.Background(), pairWeather); !ok || d.Reason != "Duplicate" {
		t.Errorf("discard stored %+v (ok %v), want the reason", d, ok)
	}
	p.press("undo")
	p.waitText(".pair-actions", "Accept pair")
	if got := p.rowStatus(pairWeather); got != "unreviewed · fix chosen" {
		t.Errorf("after undo the row says %q, want unreviewed", got)
	}
	if got := p.counts("#pair-tabs"); got["Discarded"] != 0 || got["Unreviewed"] != 3 {
		t.Errorf("after undo tabs = %v", got)
	}
	none("discard undone")
}

// Review walks the day's cuts in order with Prev and Next, and each one's
// clips and inspector are its own.
//
// verifies SPEC §9.2
func TestE2EJourneyReviewWalksEveryCut(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/review")
	for i, c := range []struct {
		title, sub, inspector string
		clips                 []float64
	}{
		// Alan cut the playlist at 17600 frames of a 3.5 s announcement, before
		// the model finished: no completion, so no model to credit.
		{"and put on some jazz", "pair 1 of 3", "unattributed: the turn recorded no completion", []float64{1.1, 2.4, 0.7, 0.9, 1.5}},
		{"play something by zeppelin", "pair 2 of 3", "in effect: qwen3-32b@1 · sys@3 · tools@7", []float64{0.8, 5.4, 0.5, 0.9, 1.5}},
		{"what's the weather today", "pair 3 of 3", "correction · teagan: do I need an umbrella", []float64{1.5, 2.5, 0.6, 1.1, 1.5}},
	} {
		if i > 0 {
			p.follow(`.page-head a.btn[href^="/review?pair="]:last-child`)
		}
		p.waitText("h1", c.title)
		p.waitText(".page-head", c.sub)
		p.waitText(".inspector", c.inspector)
		closeTo(t, c.title, p.durations(`[aria-label="Clips"]`), c.clips)
		p.shot(fmt.Sprintf("journey-review-%d", i+1))
	}
	// Last pair: no Next. Prev goes back.
	if strings.Contains(p.text(".page-head"), "Next") {
		t.Error("the last pair still offers Next")
	}
	p.follow(`.page-head a.btn[href^="/review?pair="]`)
	p.waitText("h1", "play something by zeppelin")

	// The jazz cut also dropped a queued sentence nobody heard; Review keeps
	// it in the inspector's unheard tail, not the clip list.
	p.visit(reviewHref(pairJazz))
	p.waitText(".inspector", "Say skip to hear the next one.")
}

// Every signal in the queue opens where it happened, and from that log the
// reviewer can go on to Replay and back to the day.
//
// verifies SPEC §9.2
func TestE2EJourneyEverySignalOpensWhereItHappened(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	for _, c := range []struct {
		tab, conv, anchor, why string
	}{
		{"failure", convGarage, "seq-4", "ha_get_state timed_out"},
		{"speaker-flip", convJazz, "seq-9", "alice → alan"},
		{"repeated", convTimer, "seq-7", "asked again 6.2 s after “set a timer for the oven” · no tool call on the first ask"},
	} {
		p.visit("/queue?tab=" + c.tab)
		p.follow(`#queue a.list__row`)
		if got, want := p.path(), conversationHref(c.conv); !strings.HasPrefix(got, want) {
			t.Errorf("%s row landed on %s, want %s", c.tab, got, want)
		}
		if id, visible := p.target(); id != c.anchor || !visible {
			t.Errorf("%s row targets #%s (visible %v), want #%s in view", c.tab, id, visible, c.anchor)
		}
		p.waitText("#"+c.anchor, c.why)
		p.shot("journey-signal-" + c.tab)
	}

	// From Alan and Alice's evening: the log, then Replay, then back.
	p.visit(conversationHref(convJazz))
	p.waitText(".page-head", "alice · living_room")
	p.follow(fmt.Sprintf(`.page-head a[href="%s"]`, replayHref(convJazz)))
	p.waitText("h1", "dim the living room lights")
	p.follow(fmt.Sprintf(`.page-head a[href="%s"]`, conversationHref(convJazz)))
	p.follow(`.page-head a[href="/conversations?day=2025-10-09"]`)
	p.waitText("h2", "Thursday 9 October")

	// Every barge-in row opens its own pair, not just the first.
	for _, id := range []string{pairJazz, pairZeppel, pairWeather} {
		p.visit("/queue?tab=barge-in")
		p.follow(fmt.Sprintf(`#queue a[href="%s"]`, reviewHref(id)))
		var shown string
		p.eval(`document.querySelector(".pair-actions").id`, &shown)
		if want := "pair-" + strings.NewReplacer("/", "-").Replace(id); shown != want {
			t.Errorf("queue row for %s opened %s", id, shown)
		}
	}
}

// Browse lays out the day by satellite, follows Alice from the kitchen to
// the living room, and walks back to the night before.
//
// verifies SPEC §9.2
func TestE2EJourneyBrowseFollowsThePersonAndWalksTheDays(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/conversations")
	p.waitText("h2", "Thursday 9 October")
	var today bool
	p.eval(`document.querySelector(".band").textContent.includes("today · so far")`, &today)
	if !today {
		t.Error("Browse does not mark today as today")
	}
	var lanes []string
	p.eval(`[...document.querySelectorAll(".day-lanes__name")].map(e => e.textContent)`, &lanes)
	if fmt.Sprint(lanes) != "[kitchen living_room office]" {
		t.Errorf("lanes = %v, want kitchen, living_room, office", lanes)
	}
	// Sessions: weather, zeppelin, the timer and its going off in the
	// kitchen; zeppelin's second half and jazz in the living room; garage and
	// the list in the office.
	if n := p.count(".day-lanes__session"); n != 8 {
		t.Errorf("%d sessions on the lanes, want 8", n)
	}
	// Flagged: every session that raised a signal. Zeppelin's living-room
	// half and the shopping list raised none.
	if n := p.count(".day-lanes__session.is-flagged"); n != 5 {
		t.Errorf("%d flagged sessions, want 5", n)
	}
	if n := p.count(".day-lanes__reject"); n != 2 {
		t.Errorf("%d rejected wakes, want the dishwasher and the podcast", n)
	}
	p.waitText(".day-lanes", "moved rooms · same conversation")
	p.waitText(".band:not(.row) .cap", "7 conversations")
	p.waitText("#conversations", "alice · kitchen → living_room")
	if strings.Contains(p.text("#conversations"), "lock the front door") {
		t.Error("yesterday's conversation is listed today")
	}
	// Today has no tomorrow.
	var nextDisabled bool
	p.eval(`document.querySelector('button[aria-label="Next day"]').disabled`, &nextDisabled)
	if !nextDisabled {
		t.Error("Next day is offered from today")
	}
	p.shot("journey-browse-today")

	// The Zeppelin conversation, both rooms.
	p.follow(fmt.Sprintf(`#conversations a[href="%s"]`, conversationHref(convZeppel)))
	p.waitText(".page-head", "alice · kitchen → living_room")
	p.waitText("section[aria-label=Journal]", "closed: migrated")
	p.waitText("#seq-18", "opened on living_room")
	p.waitText("#seq-18", "resumed")
	var heard float64
	p.eval(`new Promise((ok, no) => {
		const a = document.querySelector('#seq-7 audio');
		a.addEventListener("loadedmetadata", () => ok(a.duration), {once: true});
		a.addEventListener("error", () => no(new Error("cannot decode " + a.src)), {once: true});
		a.preload = "metadata"; a.load();
	})`, &heard)
	if math.Abs(heard-0.8) > 0.01 {
		t.Errorf("the cut turn plays %.3fs, want the 0.8s the DAC reached", heard)
	}
	p.shot("journey-browse-zeppelin")

	// Back to the day, then the night before.
	p.follow(`.page-head a[href="/conversations?day=2025-10-09"]`)
	p.follow(`a[aria-label="Previous day"]`)
	p.waitText("h2", "Wednesday 8 October")
	p.waitText(".band:not(.row) .cap", "1 conversation")
	p.waitText("#conversations", "lock the front door")
	if n := p.count(".day-lanes__name"); n != 1 {
		t.Errorf("the night before has %d lanes, want only the kitchen", n)
	}
	p.shot("journey-browse-yesterday")
	p.follow(`a[aria-label="Next day"]`)
	p.waitText("h2", "Thursday 9 October")

	// A week earlier: nothing happened.
	p.visit("/conversations?day=2025-10-02")
	p.waitText("body", "No conversations this day.")
}

// Twelve minutes after Alan set it, the oven timer goes off in an empty
// kitchen. Browse lists it as an announcement nobody woke, its log says why
// it was said and plays it, and the conversation that set it shows the timer
// being set, from the house log, beside the call that set it.
//
// verifies SPEC §4, §9.2
func TestE2EJourneyTheOvenTimerGoesOffInTheKitchen(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/conversations")
	oven := fmt.Sprintf(`#conversations a[href="%s"]`, conversationHref(convOven))
	for _, want := range []string{"announcement", "The oven timer is done.", "no wake word", "for alan · kitchen", "12:22"} {
		p.waitText(oven, want)
	}
	p.shot("journey-oven-browse")

	p.follow(oven)
	p.waitText(".page-head", "for alan · kitchen")
	p.waitText("#seq-1", "opened on kitchen to announce")
	p.waitText("#seq-1", "no wake word")
	p.waitText("#seq-2", "The oven timer is done.")
	p.waitText("#seq-2", "timer "+household.OvenTimer+" went off")
	p.waitText("#house-2", "went off: announced")
	p.waitText("#seq-6", "closed: announced")
	closeTo(t, "the oven going off", p.durations("#seq-4"), []float64{1.5})
	p.shot("journey-oven-goes-off")

	p.visit(conversationHref(convTimer))
	p.waitText("#house-1", "set the oven timer for 12m0s on kitchen")
	p.waitText("#house-1", "goes off 2025-10-09T12:22:07.1Z")
	var order []string
	p.eval(`[...document.querySelectorAll("section[aria-label=Journal] > div")].map(e => e.id)`, &order)
	if i := slices.Index(order, "house-1"); i < 1 || order[i-1] != "seq-8" {
		t.Errorf("rows = %v, want the timer set just after the call that set it, seq-8", order)
	}
	p.shot("journey-oven-timer-set")
}

// failsOn answers like the scripted engine until it reaches one utterance,
// then the stream dies the way Ollama's does: finish_reason error.
type failsOn struct {
	*scripted
	text string
}

func (f failsOn) Turn(ctx context.Context, in sess.Input) (<-chan sess.Action, error) {
	if in.Text != f.text {
		return f.scripted.Turn(ctx, in)
	}
	out := make(chan sess.Action, 1)
	out <- sess.TurnEnd{FinishReason: "error", Completion: `{"error":"model runner has unexpectedly stopped"}`}
	close(out)
	return out, nil
}

// asksWhichPlaylist is the scripted engine after a prompt edit that makes
// it confirm before playing to a room with two people in it.
func asksWhichPlaylist() *scripted {
	return &scripted{answers: map[string][]sess.Action{
		"dim the living room lights": {
			sess.ToolCall{ID: "c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_on","entity_id":"light.living_room","data":{"brightness_pct":30}}`},
			sess.SpeechDelta{CallID: "s1", Text: "Dimmed to thirty percent.", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		"and put on some jazz": {
			sess.ToolCall{ID: "c2", Tool: "media_search", Args: `{"query":"jazz","media_type":"playlist","limit":3}`},
			sess.SpeechDelta{CallID: "s2", Text: "Late Night Jazz or something quieter?", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		"something quieter": {
			sess.ToolCall{ID: "c3", Tool: "media_search", Args: `{"query":"quiet jazz piano","media_type":"playlist","limit":3}`},
			sess.ToolCall{ID: "c4", Tool: "ha_call_service", Args: `{"domain":"media_player","service":"play_media","entity_id":"media_player.living_room","data":{"media_content_id":"playlist:quiet-jazz-piano"}}`},
			sess.SpeechDelta{CallID: "s3", Text: "Playing Quiet Jazz Piano.", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
	}}
}

const confirmPrompt = "When two people are in the room, confirm before playing anything."

func (p *page) editPrompt(line string) {
	p.t.Helper()
	p.run(
		chromedp.Evaluate(`(() => { const a = document.querySelector("#replay-prompt"); a.value = a.value.trimEnd() + "\n\n"; })()`, nil),
		chromedp.SendKeys("#replay-prompt", line, chromedp.ByQuery),
	)
}

// The jazz evening, re-run under a prompt that asks before playing. Only the
// turn the edit was for changes, and the page says so turn by turn.
//
// verifies SPEC §9.2
func TestE2EJourneyReplayTheJazzEveningUnderAnEditedPrompt(t *testing.T) {
	hh := asksWhichPlaylist()
	s, _ := newHouseholdServer(t)
	s.engineFor = hh.engineFor
	p := open(t, s)

	p.visit("/replays")
	var order []string
	p.eval(`[...document.querySelectorAll("#replays a.list__row")].map(a => new URL(a.href).pathname)`, &order)
	want := []string{convList, convJazz, convTimer, convGarage, convZeppel, convWeather, convLock}
	for i := range want {
		want[i] = replayHref(want[i])
	}
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("replays = %v, want newest first %v", order, want)
	}

	p.follow(fmt.Sprintf(`#replays a[href="%s"]`, replayHref(convJazz)))
	if got := p.metrics(`[aria-label="Recorded"]`)["Journal replay"]; got != "replay() reads back all 26 events" {
		t.Errorf("journal replay check = %q", got)
	}
	if n := p.count(`#replay-result [id^="turn-"]`); n != 3 {
		t.Errorf("%d turns on the page, want dim, jazz, quieter", n)
	}
	p.waitText("#turn-9", "Playing Late Night Jazz")
	p.waitText("#turn-9", "Say skip to hear the next one.")

	// Hold the model so the in-flight state is visible.
	hh.gate = make(chan struct{})
	release := sync.OnceFunc(func() { close(hh.gate) })
	t.Cleanup(release) // a failure below must not leave the handler waiting
	p.editPrompt(confirmPrompt)
	p.click(`form button[type="submit"]`)
	var busy bool
	if err := chromedp.Run(p.ctx, chromedp.Poll(`(() => {
		const b = document.querySelector('form button[type="submit"]');
		const i = document.querySelector(".htmx-indicator");
		return b.disabled && getComputedStyle(i).opacity === "1";
	})()`, &busy, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		t.Error("while the model thinks, the run button should be disabled and say it is asking")
	}
	p.shot("journey-replay-running")
	release()

	p.waitText("#replay-result", "Late Night Jazz or something quieter?")
	tags := map[string]string{}
	p.eval(`Object.fromEntries([...document.querySelectorAll('#replay-result [id^="turn-"]')].map(r => [r.id, r.lastElementChild.textContent.trim()]))`, &tags)
	for turn, w := range map[string]string{"turn-2": "same", "turn-9": "speech changed", "turn-17": "same"} {
		if tags[turn] != w {
			t.Errorf("%s is tagged %q, want %q (all %v)", turn, tags[turn], w, tags)
		}
	}
	stats := p.metrics(`#replay-result [aria-label="Outcome"]`)
	if stats["Turns re-run"] != "3 of 3" || stats["Speech changed"] != "1 of 3" || stats["Tool calls changed"] != "0 of 3" {
		t.Errorf("outcome = %v", stats)
	}
	p.waitText(".code-diff", "+ "+confirmPrompt)
	if got := hh.built[len(hh.built)-1]; !strings.HasSuffix(got, confirmPrompt) || !strings.HasPrefix(got, "qwen3-32b@1|") {
		t.Errorf("the engine was built with %q", got)
	}
	var enabled bool
	p.eval(`!document.querySelector('form button[type="submit"]').disabled`, &enabled)
	if !enabled {
		t.Error("the run button stays disabled after the run")
	}
	p.shot("journey-replay-done")
}

// The ways a re-run goes wrong all land on the page as a sentence, never as
// a button that silently did nothing.
//
// verifies SPEC §9.2
func TestE2EJourneyReplayFailuresSaySoOnThePage(t *testing.T) {
	hh := asksWhichPlaylist()
	s, _ := newHouseholdServer(t)
	engine := "ok"
	s.engineFor = func(model, prompt string) (sess.Engine, journal.Versions, error) {
		eng, v, err := hh.engineFor(model, prompt)
		switch engine {
		case "dies":
			return failsOn{hh, "and put on some jazz"}, v, err
		case "unknown model":
			return nil, v, errors.New(`ollama: model "qwen3-70b@1" not found, try pulling it first`)
		}
		return eng, v, err
	}
	p := open(t, s)
	p.visit(replayHref(convJazz))

	// The model dies on Alan's ask: the first turn ran, the rest did not.
	engine = "dies"
	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result .alert", "Re-run stopped at #9.")
	p.waitText("#replay-result .alert", "model runner has unexpectedly stopped")
	if got := p.metrics(`#replay-result [aria-label="Outcome"]`)["Turns re-run"]; got != "1 of 3" {
		t.Errorf("turns re-run = %q, want 1 of 3", got)
	}
	if n := p.count(`#replay-result .list__detail`); n == 0 || !strings.Contains(p.text("#turn-17"), "not re-run") {
		t.Errorf("the turn after the failure should say it was not re-run: %q", p.text("#turn-17"))
	}
	p.shot("journey-replay-died")

	// A model that is not pulled.
	engine = "unknown model"
	p.typeInto("#replay-model", "qwen3-70b@1")
	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result .alert", "Cannot build the engine.")
	p.waitText("#replay-result .alert", "try pulling it first")

	// An emptied prompt.
	engine = "ok"
	p.typeInto("#replay-model", "qwen3-32b@1")
	p.run(chromedp.Evaluate(`document.querySelector("#replay-prompt").value = "  "`, nil))
	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result .alert", "Nothing to run.")

	// And it still runs once the prompt is back.
	p.run(chromedp.Evaluate(fmt.Sprintf(`document.querySelector("#replay-prompt").value = %q`, ollama.DefaultPrompt), nil))
	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result", "Late Night Jazz or something quieter?")
	if p.has("#replay-result .alert") {
		t.Errorf("a clean run still shows an alert: %q", p.text("#replay-result .alert"))
	}
}

// Links into the journal that point at nothing get a plain refusal, and a
// bad day in the address bar is refused rather than guessed at.
//
// verifies SPEC §9.2
func TestE2EDeadLinksAreRefused(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)
	p.expect(404, "/favicon.ico") // asked for by the plain-text refusals
	for _, c := range []struct {
		path   string
		status int
		says   string
	}{
		{"/conversations/conv-0000-attic", 404, "404 page not found"},
		{"/replays/conv-0000-attic", 404, "404 page not found"},
		{"/conversations?day=thursday", 400, "day: want YYYY-MM-DD"},
		{"/audio?ref=blob://mic/never-recorded", 404, "404 page not found"},
	} {
		p.expect(c.status, c.path)
		p.visit(c.path)
		p.waitText("body", c.says)
	}
	// A pair id that was never harvested falls back to the first pair rather
	// than an empty screen.
	p.visit("/review?pair=" + url.QueryEscape("conv-0000-attic/3"))
	p.waitText("h1", "and put on some jazz")
}

// On a phone the step links fold into a menu; it still reaches every screen,
// and no screen scrolls sideways.
//
// verifies SPEC §9.2
func TestE2EPhoneWidthReachesEveryScreen(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)
	p.run(chromedp.EmulateViewport(390, 844, chromedp.EmulateMobile))

	for _, path := range []string{"/conversations", "/queue", "/review", "/replays", "/curate/pairs", "/export"} {
		p.visit("/conversations/" + convJazz)
		var hidden bool
		p.eval(`getComputedStyle(document.querySelector(".app-header__steps")).display === "none"`, &hidden)
		if !hidden {
			t.Fatal("the full step bar shows at phone width")
		}
		p.click(".app-header__menu summary")
		p.follow(fmt.Sprintf(`.app-header__menu a[href="%s"]`, path))
		if got := p.path(); got != path {
			t.Errorf("menu link to %s landed on %s", path, got)
		}
		var overflow int
		p.eval(`document.documentElement.scrollWidth - window.innerWidth`, &overflow)
		if overflow > 0 {
			t.Errorf("%s scrolls %dpx sideways at phone width", path, overflow)
		}
		p.shot("phone" + strings.ReplaceAll(path, "/", "-"))
	}
}
