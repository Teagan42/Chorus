//go:build e2e

// Browser tests for the built docs site: what a reader gets from the pages
// `task docs:build` writes, served over HTTP and driven by a headless Chrome.
// test_hooks.py proves the links are right in the HTML; these prove the
// site works as a site: search finds a decision, the architecture diagram
// draws, the review UI screenshots load and zoom. Run with `task docs:e2e`.
package docsite

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

const (
	// chromeEnv and shotsEnv mean what they do for the review UI's tier.
	chromeEnv = "CHORUS_E2E_CHROME"
	shotsEnv  = "CHORUS_E2E_SHOTS"
	// siteEnv is the built site; `task docs:e2e` builds it and sets this.
	siteEnv = "CHORUS_DOCS_SITE"
	// mermaidEnv names a local mermaid.min.js for a box with no route to
	// the CDN Material loads it from. Unset, the CDN answers.
	mermaidEnv = "CHORUS_DOCS_MERMAID"
)

type page struct {
	t    *testing.T
	ctx  context.Context
	base string

	mu       sync.Mutex
	failures []string
}

func open(t *testing.T, width, height int64) *page {
	t.Helper()
	chrome := os.Getenv(chromeEnv)
	if chrome == "" {
		t.Skipf("%s is unset; point it at a Chrome or Chromium binary", chromeEnv)
	}
	site := os.Getenv(siteEnv)
	if site == "" {
		t.Skipf("%s is unset; run `task docs:e2e`, which builds the site first", siteEnv)
	}
	if _, err := os.Stat(filepath.Join(site, "index.html")); err != nil {
		t.Fatalf("%s=%s holds no built site: %v", siteEnv, site, err)
	}
	// Under /Chorus/, where GitHub Pages serves it, so a link that only
	// works from the domain root fails here too.
	mux := http.NewServeMux()
	mux.Handle("/Chorus/", http.StripPrefix("/Chorus", http.FileServer(http.Dir(site))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(chrome),
		chromedp.NoSandbox,
		chromedp.WindowSize(int(width), int(height)),
	)
	actx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancelTab := chromedp.NewContext(actx)
	ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(func() { cancelTimeout(); cancelTab(); cancelAlloc() })

	p := &page{t: t, ctx: ctx, base: srv.URL + "/Chorus/"}
	mermaid := os.Getenv(mermaidEnv)
	chromedp.ListenTarget(ctx, func(ev any) {
		switch ev := ev.(type) {
		case *runtime.EventExceptionThrown:
			p.fail("script error: %s", ev.ExceptionDetails.Error())
		case *network.EventResponseReceived:
			// Only our own pages: fonts and the CDN are not the site.
			if ev.Response.Status >= 400 && strings.HasPrefix(ev.Response.URL, srv.URL) {
				p.fail("%d from %s", ev.Response.Status, ev.Response.URL)
			}
		case *network.EventRequestWillBeSent:
			// The header's facts are baked in at build time; asking GitHub is
			// how a tab ended up showing a release older than the site.
			if strings.HasPrefix(ev.Request.URL, "https://api.github.com/") {
				p.fail("asked the GitHub API: %s", ev.Request.URL)
			}
		case *fetch.EventRequestPaused:
			go p.serveMermaid(ev, mermaid)
		}
	})
	// Emulated, not just a window size: headless will not size a window
	// below 500px, so a phone width needs the viewport set outright.
	actions := []chromedp.Action{network.Enable(), chromedp.EmulateViewport(width, height)}
	if mermaid != "" {
		actions = append(actions, fetch.Enable().WithPatterns([]*fetch.RequestPattern{{URLPattern: "*mermaid*.js"}}))
	}
	if err := chromedp.Run(ctx, actions...); err != nil {
		t.Fatalf("start browser: %v", err)
	}
	t.Cleanup(p.check)
	return p
}

func (p *page) serveMermaid(ev *fetch.EventRequestPaused, path string) {
	body, err := os.ReadFile(path)
	if err != nil {
		p.fail("%s: %v", mermaidEnv, err)
		return
	}
	ctx := chromedp.FromContext(p.ctx)
	exec := cdp.WithExecutor(p.ctx, ctx.Target)
	err = fetch.FulfillRequest(ev.RequestID, 200).
		WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/javascript"}}).
		WithBody(base64.StdEncoding.EncodeToString(body)).Do(exec)
	if err != nil {
		p.fail("serve mermaid: %v", err)
	}
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

func (p *page) visit(path string) {
	p.t.Helper()
	p.run(chromedp.Navigate(p.base+path), chromedp.WaitReady("body"))
}

func (p *page) eval(js string, out any) {
	p.t.Helper()
	p.run(chromedp.Evaluate(js, out, func(e *runtime.EvaluateParams) *runtime.EvaluateParams {
		return e.WithAwaitPromise(true)
	}))
}

// waitFor polls a script until it returns true. A navigation mid-poll tears
// down the context the script runs in, so an error is retried, not fatal.
func (p *page) waitFor(what, js string) {
	p.t.Helper()
	var err error
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		var ok bool
		if err = chromedp.Run(p.ctx, chromedp.Evaluate(js, &ok)); err == nil && ok {
			return
		}
	}
	p.t.Fatalf("never saw %s (last error: %v)", what, err)
}

func (p *page) shot(name string) {
	p.t.Helper()
	dir := os.Getenv(shotsEnv)
	if dir == "" {
		return
	}
	var png []byte
	p.run(chromedp.CaptureScreenshot(&png))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), png, 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// ------------------------------------------------------------------- tests

func TestE2EHomeIsTheReadmeWithItsDiagramDrawn(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("")
	var h1 string
	p.eval(`document.querySelector("article h1").firstChild.textContent.trim()`, &h1)
	if h1 != "Chorus" {
		t.Errorf("home h1 = %q, want the README's", h1)
	}
	// Material draws into a closed shadow root, so the drawing is seen by
	// the room it takes: an undrawn block is one line of source.
	p.waitFor("the architecture diagram drawn",
		`(() => { const m = document.querySelector("article div.mermaid"); return !!m && m.getBoundingClientRect().height > 100 })()`)
	p.run(chromedp.ScrollIntoView("article .mermaid", chromedp.ByQuery))
	p.shot("site-home")
}

func TestE2ESearchFindsTheBargeInDecision(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("")
	p.run(chromedp.SendKeys(`input[data-md-component="search-query"]`, "barge-in speaker identity", chromedp.ByQuery))
	p.waitFor("the barge-in ADR in the results",
		`[...document.querySelectorAll(".md-search-result__link")].some(a => a.href.includes("adr/0004-barge-in-detection-gate/"))`)
	p.shot("site-search")
	p.eval(`document.querySelector('.md-search-result__link[href*="adr/0004-barge-in-detection-gate/"]').click()`, nil)
	p.waitFor("the ADR page", `location.pathname.endsWith("/Chorus/adr/0004-barge-in-detection-gate/")`)
	p.waitFor("the ADR's title", `document.querySelector("article h1")?.firstChild.textContent.startsWith("0004")`)
}

func TestE2EReviewUIGuideScreenshotsLoadAndZoom(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("reviewui/")
	p.waitFor("every screenshot decoded",
		`[...document.querySelectorAll("article img")].every(i => i.complete && i.naturalWidth > 0)`)
	var n int
	p.eval(`document.querySelectorAll("article img").length`, &n)
	if n < 10 {
		t.Errorf("review UI guide shows %d screenshots, want every screen's", n)
	}
	p.run(chromedp.ScrollIntoView(`img[alt="Triage"]`, chromedp.ByQuery))
	p.shot("site-reviewui")
	p.eval(`document.querySelector('img[alt="Triage"]').closest("a").click()`, nil)
	p.waitFor("the screenshot zoomed", `(() => {
		const img = document.querySelector(".glightbox-container .gslide.current.loaded .gslide-image img");
		const overlay = document.querySelector(".glightbox-container .goverlay");
		return !!img && img.complete && !!overlay && getComputedStyle(overlay).opacity === "1"
			&& !document.querySelector(".glightbox-container .gslide.current[class*='-in'], .glightbox-container .gslide.current.ginlined-in");
	})()`)
	p.shot("site-reviewui-zoom")
}

func TestE2EHardwareDesignDrawsItsDiagramsAndLinksThePinMap(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("hardware/")
	p.waitFor("both diagrams drawn",
		`(() => { const ms = [...document.querySelectorAll("article div.mermaid")]; return ms.length === 2 && ms.every(m => m.getBoundingClientRect().height > 100) })()`)
	// The pin map is not a page: it is the source the schematic's labels come
	// from, so the reader lands on it at the commit the site was built from.
	var hrefs []string
	p.eval(`[...document.querySelectorAll("article a")].map(a => a.href).filter(h => h.includes("pins.yaml"))`, &hrefs)
	if len(hrefs) == 0 {
		t.Fatal("the hardware design no longer links its pin map")
	}
	for _, h := range hrefs {
		if !strings.HasPrefix(h, "https://github.com/Teagan42/Chorus/") || !strings.HasSuffix(h, "/hardware/chorus-main/pins.yaml") {
			t.Errorf("pin map linked as %q, want the source on GitHub", h)
		}
	}
	var adr string
	p.eval(`document.querySelector('article a[href*="adr/0055-"]').href`, &adr)
	if !strings.HasSuffix(adr, "/Chorus/adr/0055-the-satellite-is-a-bought-satellite1-kit-and-chorus-draws-only-the-board-under-it/") {
		t.Errorf("ADR-0055 linked as %q, want its page on the site", adr)
	}
	p.run(chromedp.ScrollIntoView("article .mermaid", chromedp.ByQuery))
	p.shot("site-hardware")
}

func TestE2EDarkModeFollowsTheToggle(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("SPEC/")
	var before string
	p.eval(`document.body.getAttribute("data-md-color-scheme")`, &before)
	p.eval(`document.querySelector('label.md-header__button[for^="__palette"]:not([hidden])').click()`, nil)
	p.waitFor("the other scheme",
		fmt.Sprintf(`document.body.getAttribute("data-md-color-scheme") !== %q`, before))
	p.shot("site-spec-dark")
}

func TestE2EContributingEditsTheRealFile(t *testing.T) {
	p := open(t, 1280, 900)
	p.visit("contributing/")
	var href string
	p.eval(`document.querySelector('a.md-content__button[title="Edit this page"]').href`, &href)
	if want := "https://github.com/Teagan42/Chorus/edit/main/CONTRIBUTING.md"; href != want {
		t.Errorf("edit link = %q, want %q", href, want)
	}
	// The README's links to CONTRIBUTING.md land on this page, not GitHub.
	p.visit("")
	var links []string
	p.eval(`[...document.querySelectorAll("article a")].filter(a => a.innerText.includes("CONTRIBUTING")).map(a => a.href)`, &links)
	if len(links) == 0 {
		t.Fatal("the home page no longer links CONTRIBUTING")
	}
	for _, l := range links {
		if l != p.base+"contributing/" {
			t.Errorf("home links CONTRIBUTING as %q, want %q", l, p.base+"contributing/")
		}
	}
}

func TestE2EPhoneWidthHasNoSidewaysScroll(t *testing.T) {
	p := open(t, 390, 844)
	for _, path := range []string{"", "reviewui/", "hardware/", "adr/0035-the-first-played-frame-is-an-event/"} {
		p.visit(path)
		var overflow bool
		p.eval(`document.documentElement.scrollWidth > document.documentElement.clientWidth`, &overflow)
		if overflow {
			t.Errorf("%q scrolls sideways at phone width", "/Chorus/"+path)
		}
	}
	p.visit("reviewui/")
	p.shot("site-phone-reviewui")
}

// shipped is the release the site was built from, as the header names it.
func shipped(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../../.release-please-manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]string
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatalf("release manifest: %v", err)
	}
	return "v" + manifest["."]
}

func TestE2EHeaderNamesTheReleaseThatShipped(t *testing.T) {
	want := shipped(t)
	p := open(t, 1280, 900)
	p.visit("")
	p.waitFor("the header's version",
		fmt.Sprintf(`document.querySelector(".md-source__fact--version")?.innerText === %q`, want))

	// A tab opened before the release, holding what GitHub said back then.
	p.eval(`sessionStorage.setItem("/Chorus/.__source", JSON.stringify({version: "v0.9.0", stars: 0, forks: 0})), true`, nil)
	p.run(chromedp.Reload())
	p.waitFor("the header's version after a reload",
		fmt.Sprintf(`document.querySelector(".md-source__fact--version")?.innerText === %q`, want))

	// And on the next page, which instant navigation loads without a reload.
	p.eval(`document.querySelector('a[href="changelog/"], a[href$="/changelog/"]').click()`, nil)
	p.waitFor("the changelog", `location.pathname.endsWith("/Chorus/changelog/")`)
	p.waitFor("the header's version on the changelog",
		fmt.Sprintf(`document.querySelector(".md-source__fact--version")?.innerText === %q`, want))
	var latest string
	p.eval(`document.querySelector("article h2").firstChild.textContent.trim()`, &latest)
	if !strings.HasPrefix("v"+latest, want) {
		t.Errorf("the changelog's newest entry is %q, the header says %q", latest, want)
	}
	// The facts fade in; a shot taken mid-fade shows an empty header.
	p.waitFor("the facts faded in", `document.querySelector(".md-source__facts")?.getAnimations().length === 0`)
	p.shot("site-header-version")
}
