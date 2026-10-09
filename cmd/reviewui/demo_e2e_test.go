//go:build e2e

package main

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teaganglenn/chorus/internal/reviewui/household"
)

// The hosted demo, as GitHub Pages serves it: the site internal/tools/demosite
// builds, under the project's path rather than at the root. Every request the
// page makes outside that path is a 404 the harness fails on, so a URL the
// shell forgot to re-point cannot pass.

// demoPrefix is where Pages puts the demo: the repository's site, then /demo/.
const demoPrefix = "/Chorus/demo/"

var demoBuild = sync.OnceValues(func() (string, error) {
	dir, err := os.MkdirTemp("", "chorus-demo")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "run", "./internal/tools/demosite", "-out", dir)
	cmd.Dir = filepath.Join("..", "..")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build the demo: %w\n%s", err, out)
	}
	return dir, nil
})

// openDemo serves the built demo statically and waits for it to boot onto
// Browse: the household server runs in the tab, not behind this handler.
func openDemo(t *testing.T) *page {
	t.Helper()
	if os.Getenv(chromeEnv) == "" {
		t.Skipf("%s is unset; point it at a Chrome or Chromium binary", chromeEnv)
	}
	dir, err := demoBuild()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(demoPrefix, http.StripPrefix(demoPrefix, http.FileServer(http.Dir(dir))))
	p := openOn(t, mux)
	p.visit(demoPrefix)
	p.waitRoute(browsePath)
	return p
}

// browsePath is where the demo opens: the day, as the brand link goes.
const browsePath = "/conversations"

// waitRoute waits until the shell has rendered path, audio included.
func (p *page) waitRoute(path string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`document.documentElement.dataset.route === %q`, path)
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(20*time.Second))); err != nil {
		var at string
		p.eval(`document.documentElement.dataset.route + " " + location.hash + " " + (document.getElementById("boot-status")?.textContent || "")`, &at)
		p.t.Fatalf("the demo never showed %s (at %q): %v", path, at, err)
	}
}

// route clicks a link the shell turned into a hash route and waits for the
// screen it names.
func (p *page) route(sel, path string) {
	p.t.Helper()
	p.click(sel)
	p.waitRoute(path)
}

func (p *page) hash() string {
	p.t.Helper()
	var h string
	p.eval(`location.hash`, &h)
	return h
}

// Someone follows the link from the docs: the day loads in their tab, says
// it is a demo, and the Zeppelin conversation plays Alice's voice and the
// answer she cut off, stopped where the DAC stopped. A reload or a shared
// link lands back on the same screen.
//
// verifies SPEC §9.2
func TestE2EDemoBootsUnderThePagesPathAndPlaysTheDay(t *testing.T) {
	p := openDemo(t)

	p.waitText(".notice", "A made-up household's Thursday, running entirely in your browser.")
	p.waitText("h2", "Thursday 9 October")
	p.waitText("#conversations", "alice · kitchen → living_room")
	if n := p.count(".day-lanes__session"); n != 7 {
		t.Errorf("%d sessions on the lanes, want 7", n)
	}
	var title string
	p.eval(`document.title`, &title)
	if !strings.HasSuffix(title, "· Chorus demo") {
		t.Errorf("title = %q, want it to say demo", title)
	}
	p.shot("demo-browse")

	zeppelin := "/conversations/" + household.ConvZeppelin
	p.route(fmt.Sprintf(`#conversations a[href="#%s"]`, zeppelin), zeppelin)
	p.waitText(".page-head", "alice · kitchen → living_room")
	p.waitText("#seq-18", "opened on living_room")
	var srcs []string
	p.eval(`[...document.querySelectorAll("audio")].map(a => a.src)`, &srcs)
	if len(srcs) == 0 {
		t.Fatal("the conversation has no clips")
	}
	for _, s := range srcs {
		if !strings.HasPrefix(s, "blob:") {
			t.Errorf("clip %q is not served from the tab; a static host has no /audio", s)
		}
	}
	closeTo(t, "the cut answer", p.durations("#seq-7"), []float64{0.8})
	var loud float64
	p.eval(`(async () => {
		const a = document.querySelector("#seq-7 audio");
		const pcm = await new AudioContext().decodeAudioData(await (await fetch(a.src)).arrayBuffer());
		const x = pcm.getChannelData(0);
		return Math.sqrt(x.reduce((s, v) => s + v * v, 0) / x.length);
	})()`, &loud)
	if loud < 0.02 {
		t.Errorf("the heard half of the answer is silent (rms %.4f); the demo has no voices", loud)
	}
	p.shot("demo-conversation")

	p.run(chromedp.Reload())
	p.waitRoute(zeppelin)
	p.waitText(".page-head", "alice · kitchen → living_room")

	// Back is the day again, without the network.
	p.run(chromedp.NavigateBack())
	p.waitRoute(browsePath)
	if h := p.hash(); h != "#"+browsePath {
		t.Errorf("back landed on %q", h)
	}
}

// Teagan's weather cut, curated in the demo: guarded, fixed, accepted, and in
// the dataset preview. The notice promises a reload forgets it; it does.
//
// verifies SPEC §9.1
func TestE2EDemoCuratesTheWeatherCutAndForgetsItOnReload(t *testing.T) {
	p := openDemo(t)

	p.route(`.app-header__steps a[href="#/queue"]`, "/queue")
	p.route(`#queue-tabs a[href="#/queue?tab=barge-in"]`, "/queue?tab=barge-in")
	p.route(fmt.Sprintf(`#queue a[href="#%s"]`, reviewHref(pairWeather)), reviewHref(pairWeather))
	p.waitText("h1", "what's the weather today")
	closeTo(t, "weather clips", p.durations(`[aria-label="Clips"]`), []float64{1.5, 2.5, 0.6, 1.1, 1.5})

	p.press("accept")
	p.waitText(".pair-actions", "This chosen answer replies to the wrong prompt.")
	p.press("edit")
	p.typeInto(".pair-actions textarea", fixWeather)
	p.click(`.pair-actions__editor button[type="submit"]`)
	p.waitText(".pair-actions", "edited by you · answers the shared prompt")
	p.press("accept")
	p.waitText(".done-card", "Accepted · "+pairWeather)
	p.shot("demo-review-accepted")

	p.route(`.app-header__steps a[href="#/export"]`, "/export")
	p.waitText("body", "Download 1 row (JSONL)")
	if got := p.badge(); got != 2 {
		t.Errorf("header badge = %d after one verdict, want 2", got)
	}
	p.waitText("body", fixWeather)
	p.shot("demo-export")

	p.run(chromedp.Reload())
	p.waitRoute("/export")
	p.waitText("body", "Download 0 rows (JSONL)")
	if got := p.badge(); got != 3 {
		t.Errorf("header badge = %d after a reload, want all 3 pairs unreviewed again", got)
	}
}

// Replay with no model behind it: the default prompt reproduces the Zeppelin
// morning exactly, and an edited one gets the brief answer the household's
// model gave once told to lead with the count.
//
// verifies SPEC §9.2
func TestE2EDemoReplaysTheZeppelinMorningUnderAnEditedPrompt(t *testing.T) {
	p := openDemo(t)

	replay := replayHref(household.ConvZeppelin)
	p.route(`.app-header__steps a[href="#/replays"]`, "/replays")
	p.route(fmt.Sprintf(`#replays a[href="#%s"]`, replay), replay)
	if p.has(".alert") {
		t.Errorf("the demo asks for a model: %q", p.text(".alert"))
	}

	p.click(`form button[type="submit"]`)
	p.waitText(`#replay-result [aria-label="Outcome"]`, "0 of 3")
	p.waitText("#turn-2", "same")

	p.editPrompt("When there are several results, say how many, offer the first, and stop.")
	p.click(`form button[type="submit"]`)
	p.waitText("#turn-2", "I found three albums. Want Led Zeppelin one?")
	p.waitText("#turn-2", "speech changed")
	p.waitText("#replay-result", "+ When there are several results, say how many")
	p.shot("demo-replay")
}

// A link into the journal that points at nothing gets the server's refusal
// on the page, not a download of it.
func TestE2EDemoRefusesADeadLinkOnThePage(t *testing.T) {
	p := openDemo(t)
	p.run(chromedp.Evaluate(`location.hash = "#/conversations/conv-0000-attic"`, nil))
	p.waitRoute("/conversations/conv-0000-attic")
	p.waitText("h1", "404 page not found")
	p.route(`a[href="#/conversations"]`, browsePath)
}

// On a phone the notice wraps above the folded step menu and nothing
// scrolls sideways.
func TestE2EDemoFitsAPhone(t *testing.T) {
	p := openDemo(t)
	p.run(chromedp.EmulateViewport(390, 844, chromedp.EmulateMobile))
	var overflow int
	p.eval(`document.documentElement.scrollWidth - window.innerWidth`, &overflow)
	if overflow > 0 {
		t.Errorf("the demo scrolls %dpx sideways at phone width", overflow)
	}
	p.shot("demo-phone")
}
