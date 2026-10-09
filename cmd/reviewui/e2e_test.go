//go:build e2e

// Browser tests: the review UI served for real over HTTP with the same
// in-memory fixtures as server_test.go, driven by a headless Chrome. The
// handler tests prove what the server writes; these prove what a reviewer
// gets: htmx actually loaded and swapping, audio a browser can decode, links
// that land on the screen they name. Run with `task test:e2e`.
package main

import (
	"context"
	"fmt"
	"math"
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

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/journal"
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
}

// open serves s on a loopback port and opens a fresh browser on it. Any
// uncaught script error or failed request on the page fails the test: an
// inert control is exactly the bug these tests exist for.
func open(t *testing.T, s *server) *page {
	t.Helper()
	chrome := os.Getenv(chromeEnv)
	if chrome == "" {
		t.Skipf("%s is unset; point it at a Chrome or Chromium binary", chromeEnv)
	}
	srv := httptest.NewServer(s.routes())
	t.Cleanup(srv.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.NoSandbox,
		chromedp.WindowSize(1280, 900),
		// Clips are played by script, not a click.
		chromedp.Flag("autoplay-policy", "no-user-gesture-required"),
	)
	actx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelTab := chromedp.NewContext(actx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 30*time.Second)
	t.Cleanup(func() { cancelTimeout(); cancelTab(); cancelAlloc() })

	p := &page{t: t, ctx: ctx, base: srv.URL}
	chromedp.ListenTarget(ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventExceptionThrown:
			p.fail("script error: %s", ev.ExceptionDetails.Error())
		case *network.EventResponseReceived:
			// Only our own routes: the shell also asks Google for fonts, and
			// a throttled font is not a broken screen.
			if ev.Response.Status >= 400 && strings.HasPrefix(ev.Response.URL, p.base+"/") {
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
// after the click returns.
func (p *page) waitText(sel, want string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && e.innerText.includes(%q) })()`, sel, want)
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("%s never showed %q (now: %q): %v", sel, want, p.text(sel), err)
	}
}

func (p *page) click(sel string) {
	p.t.Helper()
	p.run(chromedp.Click(sel, chromedp.ByQuery, chromedp.NodeVisible))
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
	for _, path := range []string{"/conversations", "/queue", "/review", "/curate/pairs", "/export"} {
		p.follow(fmt.Sprintf(`header nav a[href="%s"]`, path))
		if got := p.path(); got != path {
			t.Errorf("header link to %s landed on %s", path, got)
		}
	}
}
