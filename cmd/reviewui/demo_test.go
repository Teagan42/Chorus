package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/provider/ollama"
)

func newDemo(t *testing.T) *server {
	t.Helper()
	s, err := newDemoServer(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// The demo is the household's Thursday at 22:30, saying on every screen
// that it is a demo; a household's own review box says nothing of the kind.
//
// verifies SPEC §9.2
func TestDemoServesTheHouseholdDayUnderANotice(t *testing.T) {
	s := newDemo(t)
	for _, path := range []string{"/conversations", "/queue", "/review", "/replays", "/curate/pairs", "/export", "/conversations/" + convJazz} {
		if h := get(t, s, path); !strings.Contains(h, `<aside class="notice tone-conv" role="note" aria-label="Demo">`) {
			t.Errorf("%s has no demo notice", path)
		}
	}
	if h := get(t, s, "/conversations"); !strings.Contains(h, "Thursday 9 October") || !strings.Contains(h, "today · so far") {
		t.Error("the demo does not open on the household's Thursday as today")
	}
	real, _ := newHouseholdServer(t)
	if strings.Contains(get(t, real, "/conversations"), "notice") {
		t.Error("a review box that is not the demo carries the notice")
	}
}

// Replay works with no model configured: the default prompt reproduces the
// Zeppelin morning, and the brief prompt changes only Alice's first ask.
//
// verifies SPEC §9.2
func TestDemoReplaysWithoutAModel(t *testing.T) {
	s := newDemo(t)
	if h := get(t, s, replayHref(convZeppel)); strings.Contains(h, "No model to ask.") || strings.Contains(h, " disabled>Re-run every turn") {
		t.Fatal("the demo's Replay asks for a model it does not need")
	}
	form := url.Values{"model": {"qwen3-32b@1"}, "prompt": {ollama.DefaultPrompt}}
	code, h := post(t, s, replayHref(convZeppel), form)
	if code != http.StatusOK || !strings.Contains(h, "0 of 3") {
		t.Fatalf("default-prompt run = %d, want nothing changed:\n%s", code, h)
	}
	form.Set("prompt", ollama.DefaultPrompt+"\n\nWhen there are several results, say how many, offer the first, and stop.")
	_, h = post(t, s, replayHref(convZeppel), form)
	for _, want := range []string{"I found three albums. Want Led Zeppelin one?", "speech changed", "1 of 3", "sys@edited"} {
		if !strings.Contains(h, want) {
			t.Errorf("edited-prompt run does not show %q", want)
		}
	}
}

// Teagan's weather cut, fixed and accepted, is the one row the demo exports,
// and the button counts it in the singular.
//
// verifies SPEC §9.1
func TestDemoCuratesIntoTheExport(t *testing.T) {
	s := newDemo(t)
	for _, step := range []struct {
		action string
		form   url.Values
	}{{"save", url.Values{"chosen": {fixWeather}}}, {"accept", nil}} {
		if code, h := post(t, s, "/pairs/"+pairWeather+"/"+step.action, step.form); code != http.StatusOK {
			t.Fatalf("%s = %d: %s", step.action, code, h)
		}
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Download 1 row (JSONL)") {
		t.Error("the export does not offer the one accepted row")
	}
	if body := get(t, s, "/export/dpo.jsonl"); !strings.Contains(body, fixWeather) {
		t.Errorf("the download is missing the fix:\n%s", body)
	}
}

// Audio comes from the household's spoken clips, sliced where the DAC cut.
func TestDemoServesTheHeardHalfOfACutAnswer(t *testing.T) {
	s := newDemo(t)
	wav := get(t, s, "/audio?ref="+url.QueryEscape("blob://tts/zeppelin-list")+"&to=12800")
	if len(wav) != 44+12800*2 {
		t.Errorf("heard half is %d bytes, want a WAV header and 12800 frames", len(wav))
	}
}
