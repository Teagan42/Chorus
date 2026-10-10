//go:build e2e

// Browser tests: the review UI served for real over HTTP with the same
// in-memory fixtures as server_test.go and household_test.go, driven by a
// headless Chrome. The handler tests prove what the server writes; these
// prove what a reviewer gets: htmx actually loaded and swapping, audio a
// browser can decode, links that land on the screen they name. This file
// holds the harness and per-screen checks; e2e_journeys_test.go walks a
// reviewer through a whole day. Run with `task test:e2e`.
package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
	sess "github.com/teagan42/chorus/internal/session"
)

// chromeEnv names the browser binary. Unset skips the tier, the way an unset
// DSN skips test:db; CI sets it so the tier fails rather than skips there.
const chromeEnv = "CHORUS_E2E_CHROME"

// shotsEnv, when set, is a directory each test writes a full-page PNG into.
const shotsEnv = "CHORUS_E2E_SHOTS"

// page is one browser tab on a running review UI.
type page struct {
	t    *testing.T
	ctx  context.Context
	base string

	mu       sync.Mutex
	failures []string
	allowed  map[string]int // URL → the error status a test asked for
}

// open serves s on a loopback port and opens a fresh browser on it. Any
// uncaught script error or failed request on the page fails the test: an
// inert control is exactly the bug these tests exist for.
func open(t *testing.T, s *server) *page {
	t.Helper()
	return openOn(t, s.routes())
}

// openOn is open for any handler, such as a static host serving the demo.
func openOn(t *testing.T, h http.Handler) *page {
	t.Helper()
	chrome := os.Getenv(chromeEnv)
	if chrome == "" {
		t.Skipf("%s is unset; point it at a Chrome or Chromium binary", chromeEnv)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.NoSandbox,
		chromedp.WindowSize(1280, 900),
		// Clips are played by script, not a click.
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
		// A cold Chrome on a CI runner, right after a build, can take
		// past chromedp's 20 s to come up; the test's clock starts after.
		chromedp.WSURLReadTimeout(time.Minute),
	)
	actx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	tab, cancelTab := chromedp.NewContext(actx)
	t.Cleanup(func() { cancelTab(); cancelAlloc() })
	if err := chromedp.Run(tab); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	ctx, cancelTimeout := context.WithTimeout(tab, 30*time.Second)
	t.Cleanup(cancelTimeout)

	p := &page{t: t, ctx: ctx, base: srv.URL}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventExceptionThrown:
			p.fail("script error: %s", ev.ExceptionDetails.Error())
		case *network.EventResponseReceived:
			// Only our own routes: the shell also asks Google for fonts, and
			// a throttled font is not a broken screen.
			if ev.Response.Status >= 400 && strings.HasPrefix(ev.Response.URL, p.base+"/") && !p.expected(int(ev.Response.Status), ev.Response.URL) {
				p.fail("%d from %s", ev.Response.Status, ev.Response.URL)
			}
		}
	})
	if err := chromedp.Run(ctx, network.Enable()); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	t.Cleanup(p.check)
	return p
}

func (p *page) fail(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.failures = append(p.failures, fmt.Sprintf(format, args...))
}

func (p *page) expected(status int, url string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.allowed[url] == status
}

func (p *page) check() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, f := range p.failures {
		p.t.Errorf("browser: %s", f)
	}
}

func (p *page) run(actions ...chromedp.Action) {
	p.t.Helper()
	if err := chromedp.Run(p.ctx, actions...); err != nil {
		p.t.Fatalf("browser: %v", err)
	}
}

// visit loads a path and waits for the document to be ready.
func (p *page) visit(path string) {
	p.t.Helper()
	p.run(chromedp.Navigate(p.base+path), chromedp.WaitReady("body"))
}

// eval runs a script, awaiting it if it returns a promise.
func (p *page) eval(js string, out any) {
	p.t.Helper()
	p.run(chromedp.Evaluate(js, out, func(e *runtime.EvaluateParams) *runtime.EvaluateParams {
		return e.WithAwaitPromise(true)
	}))
}

// text is the rendered text of the first match for sel.
func (p *page) text(sel string) string {
	p.t.Helper()
	var s string
	p.run(chromedp.Text(sel, &s, chromedp.ByQuery))
	return s
}

// waitText polls until sel's rendered text contains want: an htmx swap lands
// after the click returns. Case is ignored, since captions are uppercased
// by CSS and a reader does not care.
func (p *page) waitText(sel, want string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && e.innerText.toLowerCase().includes(%q) })()`, sel, strings.ToLower(want))
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("%s never showed %q (now: %q): %v", sel, want, p.text(sel), err)
	}
}

// click waits for sel to be on screen and wired, then clicks it from script.
// A mouse click at the node's coordinates races the swap the previous click
// started, and a control htmx has not processed yet swallows the click; a
// person is never that fast, so neither is this.
func (p *page) click(sel string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`(() => {
		if (document.querySelector(".htmx-request, .htmx-swapping, .htmx-settling, .htmx-added")) return false;
		const e = document.querySelector(%q);
		if (!e || !e.checkVisibility()) return false;
		const hx = [...e.attributes].some(a => a.name.startsWith("hx-")) || e.closest("form[hx-post]");
		if (hx && !(e.closest("[hx-post], [hx-get], form")["htmx-internal-data"] || {}).initHash) return false;
		e.click();
		return true;
	})()`, sel)
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("nothing on screen to click at %s: %v", sel, err)
	}
}

// follow clicks a link and waits for the page it names to finish loading.
// A script click: chromedp's mouse click re-reads the node after the press,
// and by then the navigation it caused has torn the node down.
func (p *page) follow(sel string) {
	p.t.Helper()
	js := fmt.Sprintf(`document.querySelector(%q).click()`, sel)
	resp, err := chromedp.RunResponse(p.ctx, chromedp.Evaluate(js, nil))
	if err != nil {
		p.t.Fatalf("follow %s: %v", sel, err)
	}
	if resp == nil {
		p.t.Fatalf("clicking %s navigated nowhere", sel)
	}
}

func (p *page) path() string {
	p.t.Helper()
	var loc string
	p.run(chromedp.Location(&loc))
	u, err := url.Parse(loc)
	if err != nil {
		p.t.Fatalf("location %q: %v", loc, err)
	}
	return u.RequestURI()
}

// shot saves a full-page screenshot when shotsEnv names a directory.
func (p *page) shot(name string) {
	p.t.Helper()
	dir := os.Getenv(shotsEnv)
	if dir == "" {
		return
	}
	var png []byte
	p.run(chromedp.FullScreenshot(&png, 100))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// emptyServer has a journal with nothing in it.
func emptyServer() *server {
	return newServer(journal.NewMemStore(), curation.NewMemStore(), blob.NewMemory(), time.Now)
}

// verifies SPEC §9.2
func TestE2EShellLoadsHtmx(t *testing.T) {
	s, _ := newTestServer(t)
	p := open(t, s)
	p.visit("/curate/pairs")
	var kind string
	p.eval(`typeof htmx === "object" && typeof htmx.process === "function" ? htmx.version : "missing"`, &kind)
	if kind == "missing" {
		t.Fatal("htmx did not load; every hx- control on every screen is inert")
	}
}

// verifies SPEC §9.1
func TestE2ECurateGuardsThenSavesAFix(t *testing.T) {
	s, _ := newTestServer(t)
	p := open(t, s)
	p.visit("/curate/pairs")
	p.waitText(".pair-actions", "I found three")
	p.shot("curate")

	// Accepting the as-said answer as-is trips the wrong-prompt guard.
	p.click(`.pair-actions button[hx-post$="/accept"]`)
	p.waitText(".pair-actions", "This chosen answer replies to the wrong prompt.")
	p.shot("curate-guard")

	// Reopen the pair, fix the chosen side, save.
	p.visit("/curate/pairs")
	p.click(`.pair-actions button[hx-post$="/edit"]`)
	fixed := "Playing the first Led Zeppelin album."
	p.run(
		chromedp.WaitVisible(".pair-actions textarea", chromedp.ByQuery),
		chromedp.Evaluate(`document.querySelector(".pair-actions textarea").value = ""`, nil),
		chromedp.SendKeys(".pair-actions textarea", fixed, chromedp.ByQuery),
	)
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", fixed)

	// The verdict is stored, not just drawn: a fresh load still has it.
	p.visit("/curate/pairs")
	p.waitText(".pair-actions", fixed)
}

// verifies SPEC §9.2
func TestE2EReviewPlaysEveryClipBoundedAtTheCut(t *testing.T) {
	s, _ := newTestServer(t)
	p := open(t, s)
	p.visit("/review")
	if got := p.text("h1"); !strings.Contains(got, "play something by zeppelin") {
		t.Errorf("title = %q, want the prompt the cut turn answered", got)
	}
	p.waitText(".timeline", "cut")
	p.shot("review")

	// Every clip decodes in the browser. The heard side stops at the 2080
	// frames the DAC confirmed; the unheard tail is the rest of the 2 s blob.
	var durations []float64
	p.eval(`Promise.all([...document.querySelectorAll("audio")].map(a => new Promise((ok, no) => {
		a.addEventListener("loadedmetadata", () => ok(a.duration), {once: true});
		a.addEventListener("error", () => no(new Error("cannot decode " + a.src)), {once: true});
		a.preload = "metadata"; a.load();
	})))`, &durations)
	want := []float64{0.13, 1.87, 0.5, 1, 1} // heard, unheard tail, barge-in, correction, as said
	if len(durations) != len(want) {
		t.Fatalf("clips = %v, want %d", durations, len(want))
	}
	for i := range want {
		if math.Abs(durations[i]-want[i]) > 0.01 {
			t.Errorf("clip %d lasts %.3fs, want %.2fs", i, durations[i], want[i])
		}
	}

	// And one actually plays through to its end.
	var ended bool
	p.eval(`new Promise((ok, no) => {
		const a = document.querySelectorAll("audio")[2];
		a.addEventListener("ended", () => ok(true), {once: true});
		a.play().catch(no);
	})`, &ended)
	if !ended {
		t.Error("the barge-in clip did not play to its end")
	}
}

// verifies SPEC §9.2
func TestE2ETriageFiltersAndOpensTheBargeInInReview(t *testing.T) {
	p := open(t, newTriageServer(t))
	p.visit("/queue")
	queue := p.text("#queue")
	if f, b := strings.Index(queue, "is the garage door closed"), strings.Index(queue, "play something by zeppelin"); f < 0 || b < 0 || f > b {
		t.Errorf("queue should list the failure above the barge-in:\n%s", queue)
	}
	p.shot("triage")

	p.follow(`#queue-tabs a[href$="tab=failure"]`)
	p.waitText("#queue", "is the garage door closed")
	if strings.Contains(p.text("#queue"), "zeppelin") {
		t.Error("the failures tab still lists the barge-in")
	}
	p.follow(`#queue-tabs a[href$="tab=speaker-flip"]`)
	p.waitText("#queue", "Nothing in this pile.")
	p.shot("triage-empty-tab")

	// The barge-in row deep-links to its pair on Review.
	p.visit("/queue")
	p.follow(`#queue a[href^="/review?pair="]`)
	p.waitText("h1", "play something by zeppelin")
	if got, want := p.path(), reviewHref(pairID); got != want {
		t.Errorf("landed on %s, want %s", got, want)
	}
	p.waitText(".pair-actions", "I found three")
}

// verifies SPEC §9.2
func TestE2ETriageOpensAFailureAtItsEvent(t *testing.T) {
	p := open(t, newTriageServer(t))
	p.visit("/queue?tab=failure")
	p.follow(`#queue a[href^="/conversations/"]`)
	if got := p.path(); got != "/conversations/conv-2" {
		t.Errorf("landed on %s, want the failing conversation", got)
	}
	// The row the link names is the one on screen, not just in the DOM.
	var target struct {
		ID      string
		Visible bool
	}
	p.eval(`(() => {
		const e = document.querySelector(":target");
		if (!e) return {ID: "", Visible: false};
		const r = e.getBoundingClientRect();
		return {ID: e.id, Visible: r.top >= 0 && r.bottom <= window.innerHeight};
	})()`, &target)
	if target.ID != "seq-4" || !target.Visible {
		t.Errorf("target = %+v, want #seq-4 scrolled into view", target)
	}
	p.waitText("#seq-4", "cover.state timed_out")
	p.shot("conversation-failure")
}

// Alan's oven timer: the Repeated tab holds his second ask, and the row
// opens the conversation with that ask in view.
//
// verifies SPEC §9.1
func TestE2ETriageRepeatedOpensTheSecondAsk(t *testing.T) {
	p := open(t, newRepeatServer(t))
	p.visit("/queue")
	p.follow(`#queue-tabs a[href$="tab=repeated"]`)
	p.waitText("#queue", "set a timer for twelve minutes")
	if strings.Contains(p.text("#queue"), "zeppelin") {
		t.Error("the repeated tab still lists the barge-in")
	}
	p.shot("triage-repeated")

	p.follow(`#queue a[href^="/conversations/conv-3"]`)
	var target struct {
		ID      string
		Visible bool
	}
	p.eval(`(() => {
		const e = document.querySelector(":target");
		if (!e) return {ID: "", Visible: false};
		const r = e.getBoundingClientRect();
		return {ID: e.id, Visible: r.top >= 0 && r.bottom <= window.innerHeight};
	})()`, &target)
	if target.ID != "seq-6" || !target.Visible {
		t.Errorf("target = %+v, want the second ask #seq-6 in view", target)
	}
	p.waitText("#seq-6", "asked again 6.2 s after")
}

// Teagan waited 1.7 s for the front door, and out the garage sensor's
// timeout. The slow tab holds those answers and not the shopping list,
// and opens the door's log at the frame the household first heard.
//
// verifies SPEC §11, §9.2
func TestE2ETriageSlowOpensTheFirstFrame(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)
	p.visit("/queue")
	p.follow(`#queue-tabs a[href$="tab=slow"]`)
	p.waitText("#queue", "unlock the front door")
	p.waitText("#queue", "is the garage door closed")
	if q := p.text("#queue"); strings.Contains(q, "oat milk") || strings.Contains(q, "zeppelin") {
		t.Errorf("the slow tab lists more than the slow answers: %q", q)
	}
	p.shot("triage-slow")

	p.follow(`#queue a[href^="` + conversationHref(convDoor) + `"]`)
	var target struct {
		ID      string
		Visible bool
	}
	p.eval(`(() => {
		const e = document.querySelector(":target");
		if (!e) return {ID: "", Visible: false};
		const r = e.getBoundingClientRect();
		return {ID: e.id, Visible: r.top >= 0 && r.bottom <= window.innerHeight};
	})()`, &target)
	if target.ID != "seq-8" || !target.Visible {
		t.Errorf("target = %+v, want the first frame #seq-8 in view", target)
	}
	p.waitText("#seq-8", "first audio 1.70 s after the ask")
	p.waitText("#seq-17", "first audio 0.60 s after the ask")
	p.shot("conversation-first-audio")
}

// verifies SPEC §9.2
func TestE2EBrowseOpensEachConversationAndComesBack(t *testing.T) {
	p := open(t, newBrowseServer(t))
	p.visit("/conversations")
	p.waitText("h2", "Thursday 9 October")
	for _, lane := range []string{"kitchen", "office"} {
		if !strings.Contains(p.text(".day-lanes"), lane) {
			t.Errorf("no lane for %s", lane)
		}
	}
	var flagged, rejects int
	p.eval(`document.querySelectorAll(".day-lanes__session.is-flagged").length`, &flagged)
	p.eval(`document.querySelectorAll(".day-lanes__reject").length`, &rejects)
	if flagged != 2 || rejects != 1 {
		t.Errorf("%d flagged sessions and %d rejected wakes, want 2 and 1", flagged, rejects)
	}
	p.shot("browse")

	// A session on the lane opens its conversation.
	p.follow(`.day-lanes__session[href="/conversations/conv-1"]`)
	p.waitText("body", "I found three")
	if got := p.path(); got != "/conversations/conv-1" {
		t.Errorf("landed on %s, want /conversations/conv-1", got)
	}
	// The cut turn plays only what the DAC confirmed: 2080 frames.
	var heard float64
	p.eval(`new Promise((ok, no) => {
		const a = document.querySelector('audio[src*="to=2080"]');
		if (!a) return no(new Error("no audio bounded at the cut"));
		a.addEventListener("loadedmetadata", () => ok(a.duration), {once: true});
		a.addEventListener("error", () => no(new Error("cannot decode " + a.src)), {once: true});
		a.preload = "metadata"; a.load();
	})`, &heard)
	if math.Abs(heard-0.13) > 0.01 {
		t.Errorf("heard half lasts %.3fs, want 0.13s", heard)
	}
	p.shot("conversation")

	// And back to the day it happened on, then the other one from the list.
	p.follow(`a.btn[href^="/conversations?day="]`)
	p.waitText("h2", "Thursday 9 October")
	p.follow(`a.list__row[href="/conversations/conv-2"]`)
	p.waitText("body", "is the garage door closed")
}

// saysWhatItCannot is the scripted engine after a prompt edit that has it
// say plainly what it cannot do. Offered what chorusd offers, it has no
// search, so Alice's album is one it cannot look for.
func saysWhatItCannot() *scripted {
	return &scripted{answers: map[string][]sess.Action{
		"play something by zeppelin": {
			sess.SpeechDelta{CallID: "call_1", Text: "Sorry, I can't search for music yet.", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		"just the first one": {
			sess.SpeechDelta{CallID: "call_2", Text: "Playing Led Zeppelin one.", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
	}}
}

// A reviewer opens the Zeppelin barge-in from its log, adds a line to the
// prompt, re-runs it under the tools chorusd offers and reads what the model
// would have done instead.
//
// verifies SPEC §9.2
func TestE2EReplayRerunsAnEditedPromptAndShowsWhatChanged(t *testing.T) {
	hh := saysWhatItCannot()
	p := open(t, newReplayServer(t, hh))
	p.visit("/replays")
	p.waitText("#replays", "play something by zeppelin")
	p.shot("replays")

	p.visit("/conversations/conv-1")
	p.follow(`a[href="/replays/conv-1"]`)
	p.waitText("#replay-result", "albums by that artist")
	if got := p.path(); got != "/replays/conv-1" {
		t.Errorf("landed on %s, want the conversation's replay", got)
	}
	p.shot("replay")

	edit := "When you cannot do what was asked, say so plainly."
	p.run(
		chromedp.Evaluate(`(() => { const a = document.querySelector("#replay-prompt"); a.value = a.value.trimEnd() + "\n\n"; })()`, nil),
		chromedp.SendKeys("#replay-prompt", edit, chromedp.ByQuery),
	)
	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result", "Sorry, I can't search for music yet.")
	for _, want := range []string{"speech changed", "1 of 2", "sys@edited · tools@8", edit} {
		if !strings.Contains(p.text("#replay-result"), want) {
			t.Errorf("the re-run is missing %q", want)
		}
	}
	if got := hh.built[len(hh.built)-1]; !strings.HasSuffix(got, edit) {
		t.Errorf("the model was built with %q, want the edited prompt", got)
	}
	// The page stays put: the run swapped in, it did not navigate.
	if got := p.path(); got != "/replays/conv-1" {
		t.Errorf("a re-run moved the page to %s", got)
	}
	p.shot("replay-rerun")
}

// verifies SPEC §9.2
func TestE2EReplayWithoutAModelCannotRun(t *testing.T) {
	p := open(t, newReplayServer(t, nil))
	p.visit("/replays/conv-1")
	p.waitText(".alert", "No model to ask.")
	var disabled bool
	p.eval(`document.querySelector('form button[type="submit"]').disabled`, &disabled)
	if !disabled {
		t.Error("with no model configured, the run button should be disabled")
	}
	p.shot("replay-no-model")
}

// verifies SPEC §9.1
func TestE2EExportDownloadsTheAcceptedPair(t *testing.T) {
	s, decisions := newTestServer(t)
	accept(t, decisions, "Playing the first Led Zeppelin album.")
	p := open(t, s)
	p.visit("/export")
	p.waitText("body", "Playing the first Led Zeppelin album.")
	p.shot("export")

	var rows int
	p.eval(`fetch(document.querySelector('a[href="/export/dpo.jsonl"]').href)
		.then(r => r.text()).then(t => t.trim().split("\n").filter(Boolean).length)`, &rows)
	if rows != 1 {
		t.Errorf("download has %d rows, want 1", rows)
	}
}

// verifies SPEC §9.2
func TestE2EEveryScreenSaysWhenItIsEmpty(t *testing.T) {
	p := open(t, emptyServer())
	for _, c := range []struct{ path, want string }{
		{"/curate/pairs", "Nothing in this pile."},
		{"/review", "Nothing to review."},
		{"/queue", "Nothing in this pile."},
		{"/export", "Nothing to export yet."},
		{"/conversations", "No conversations this day."},
		{"/replays", "Nothing to replay yet."},
	} {
		p.visit(c.path)
		p.waitText("body", c.want)
		p.shot(strings.Trim(strings.ReplaceAll(c.path, "/", "-"), "-") + "-empty")
	}
	// An empty export offers nothing to download.
	var disabled bool
	p.eval(`!document.querySelector('a[href="/export/dpo.jsonl"]')`, &disabled)
	if !disabled {
		t.Error("an empty export still links a download")
	}
}

// verifies SPEC §9.2
func TestE2EHeaderLinksLandOnTheirScreens(t *testing.T) {
	s, _ := newTestServer(t)
	p := open(t, s)
	p.visit("/export")
	for _, path := range []string{"/conversations", "/queue", "/review", "/replays", "/curate/pairs", "/export"} {
		p.follow(fmt.Sprintf(`header nav a[href="%s"]`, path))
		if got := p.path(); got != path {
			t.Errorf("header link to %s landed on %s", path, got)
		}
	}
}

// The kitchen's evening with Ollama down and Kokoro dying, in a browser:
// both failures read as rows that say what failed, and the forecast's cut
// is the voice's.
//
// verifies SPEC §7
func TestE2EAConversationShowsTheModelAndTheVoiceFailing(t *testing.T) {
	now := func() time.Time { return thursday.Add(20 * time.Hour) }
	p := open(t, newServer(failingKitchen(t), curation.NewMemStore(), fixtureBlobs(t), now))
	p.visit("/conversations/c-kitchen-evening")
	p.waitText("#seq-3", "model failed: unavailable")
	p.waitText("#seq-3", "apologised in cn_77d0a1b2")
	p.waitText("#seq-9", "voice failed: tts_unavailable")
	p.waitText("#seq-9", "kokoro: 503 Service Unavailable")
	p.waitText("#seq-10", "cut: tts_unavailable")
	p.shot("conversation-provider-failure")
}

// The bare address opens the household's day, and so does the brand.
//
// verifies SPEC §9.2
func TestE2ETheBareAddressLandsOnBrowse(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)
	p.visit("/")
	if got := p.path(); got != "/conversations" {
		t.Errorf("/ landed on %s, want Browse", got)
	}
	p.waitText("h2", "Thursday 9 October")
	p.shot("browse-landing")

	p.visit("/export")
	p.follow("a.app-header__brand")
	if got := p.path(); got != "/conversations" {
		t.Errorf("the brand landed on %s, want Browse", got)
	}
}

// The kitchen's own log: its radar's day and the dishwasher that woke it,
// on both channels the browser decodes, each named for a screen reader.
//
// verifies SPEC §3.3.1, §9.3
func TestE2EASatellitesOwnLogPlaysItsRejectedWake(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)
	p.visit("/conversations/device:kitchen")
	p.waitText("#seq-1", "presence: present")
	p.waitText("#seq-7", "wake rejected: no_speech")
	closeTo(t, "the dishwasher", p.durations("#seq-7"), []float64{0.8, 0.8})
	var names []string
	p.eval(`[...document.querySelectorAll("#seq-7 audio")].map(a => a.getAttribute("aria-label"))`, &names)
	if fmt.Sprint(names) != "[#7 wake rejected · device #7 wake rejected · device · second channel]" {
		t.Errorf("the dishwasher's players are named %q", names)
	}
	p.shot("conversation-device-kitchen")
}

// Teagan's rice timer went off to an unplugged office. Its Triage row opens
// the house log at the event, which says why nobody heard it.
//
// verifies SPEC §7, §9.2
func TestE2ETriageOpensTheHouseLogAtATimerNobodyHeard(t *testing.T) {
	p := open(t, newServer(withRiceTimer(t, householdJournal(t)), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt))
	p.visit("/queue?tab=failure")
	p.follow(`#queue a[href="/conversations/house:timers#seq-4"]`)
	if got := p.path(); got != "/conversations/house:timers" {
		t.Errorf("landed on %s, want the house log at the rice timer", got)
	}
	if id, visible := p.target(); id != "seq-4" || !visible {
		t.Errorf("target #%s (visible %v), want #seq-4 in view", id, visible)
	}
	p.waitText("#seq-4", "went off: unannounced")
	p.waitText("#seq-4", "office: not connected")
	p.shot("conversation-house-timers")
}

// A page served by another service on the box, open in Alan's browser,
// posts a verdict to the review UI. The browser says where it came from, so
// the post is refused and nothing is stored; the UI's own htmx posts land.
//
// verifies SPEC §9.2
func TestE2EAnotherSitesPageCannotPostAVerdict(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)
	action := "/pairs/" + pairZeppel + "/accept-anyway"
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `<!doctype html><title>Recipes</title><form method="post" action="%s%s"><button>Save the recipe</button></form>`, p.base, action)
	}))
	t.Cleanup(other.Close)

	p.expect(http.StatusForbidden, action)
	p.expect(http.StatusNotFound, "/favicon.ico") // asked for by the plain-text refusal
	p.run(chromedp.Navigate(other.URL), chromedp.WaitReady("button", chromedp.ByQuery))
	p.follow("form button")
	p.waitText("body", "cross-origin request detected")
	if d, ok, _ := decisions.Get(context.Background(), pairZeppel); ok {
		t.Errorf("another site's post stored %+v", d)
	}

	p.visit("/conversations/" + convZeppel)
	p.click(chip(zeppelinAsk, "too_slow"))
	p.pressed(chip(zeppelinAsk, "too_slow"), true)
	if a, _ := decisions.Annotations(context.Background(), convZeppel); !a[zeppelinAsk].Has(curation.LabelTooSlow) {
		t.Errorf("the UI's own label post stored %+v", a)
	}
}

// A log no reducer can read is left out with a note naming it, and the rest
// of the household's day is there to review.
//
// verifies SPEC §8, §9.2
func TestE2EAnUnreadableLogIsNamedAndTheDayGoesOn(t *testing.T) {
	p := open(t, newServer(withUnreadableRecall(t, householdJournal(t)), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt))
	for _, c := range []struct{ screen, path, list string }{
		{"browse", "/conversations", "#conversations"},
		{"triage", "/queue", "#queue"},
		{"replays", "/replays", "#replays"},
	} {
		p.visit(c.path)
		p.waitText(".alert", "Skipped 1 log that would not read.")
		p.waitText(".alert", convCalendar)
		p.waitText(c.list, "play something by zeppelin")
		p.shot(c.screen + "-skipped-log")
	}
}
