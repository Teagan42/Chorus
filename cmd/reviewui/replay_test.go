package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/provider/ollama"
	sess "github.com/teaganglenn/chorus/internal/session"
)

// cutFirstPrompt is the edit a reviewer makes after the Zeppelin barge-in:
// lead with the count, offer the first, stop.
const cutFirstPrompt = ollama.DefaultPrompt + "\n\nWhen there are several results, say how many, offer the first, and stop."

// scripted is a turn engine that answers the way the model does after the
// prompt edit: it leads with the count and searches before it speaks. It
// keeps what each re-run was built with.
type scripted struct {
	built   []string // model + "|" + prompt, per engine built
	answers map[string][]sess.Action
	fail    error
	gate    chan struct{} // when set, a turn waits on it before answering
}

func (h *scripted) engineFor(model, prompt string) (sess.Engine, journal.Versions, error) {
	h.built = append(h.built, model+"|"+prompt)
	v := journal.Versions{Model: model, Prompt: "sys@edited", ToolSchema: "tools@7"}
	if prompt == ollama.DefaultPrompt {
		v.Prompt = "sys@3"
	}
	return h, v, nil
}

func (h *scripted) Turn(ctx context.Context, in sess.Input) (<-chan sess.Action, error) {
	if h.gate != nil {
		select {
		case <-h.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if h.fail != nil {
		return nil, h.fail
	}
	out := make(chan sess.Action, len(h.answers[in.Text]))
	for _, a := range h.answers[in.Text] {
		out <- a
	}
	close(out)
	return out, nil
}

func leadsWithTheCount() *scripted {
	return &scripted{answers: map[string][]sess.Action{
		"play something by zeppelin": {
			sess.ToolCall{ID: "call_1", Tool: "media_search", Args: `{"query":"Led Zeppelin","limit":5}`},
			sess.SpeechDelta{CallID: "call_2", Text: "I found three albums. ", Mode: sess.ModeQueue},
			sess.SpeechDelta{CallID: "call_2", Text: "Want Led Zeppelin one?", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		"just the first one": {
			sess.SpeechDelta{CallID: "call_3", Text: "Playing Led Zeppelin one.", Mode: sess.ModeQueue, Last: true},
			sess.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
	}}
}

func newReplayServer(t *testing.T, h *scripted) *server {
	t.Helper()
	s := newServer(withWakeReject(t, withFailure(t, bargeInLog(t))), curation.NewMemStore(), fixtureBlobs(t), func() time.Time { return time.Unix(1_760_010_000, 0).UTC() })
	if h != nil {
		s.engineFor = h.engineFor
	}
	return s
}

// runReplay posts a re-run the way the page's form does, through htmx.
func runReplay(t *testing.T, s *server, id string, form url.Values) string {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, replayHref(id), strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("HX-Request", "true")
	s.routes().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("POST %s = %d: %s", replayHref(id), w.Code, w.Body.String())
	}
	return w.Body.String()
}

func missing(t *testing.T, page, h string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(h, want) {
			t.Errorf("%s is missing %q", page, want)
		}
	}
}

// verifies SPEC §9.2
func TestReplayListsEveryConversationToReplay(t *testing.T) {
	h := get(t, newReplayServer(t, nil), "/replays")
	missing(t, "Replay index", h,
		`href="/replays/conv-1"`, "play something by zeppelin",
		`href="/replays/conv-2"`, "is the garage door closed",
		"qwen3-32b@1", // what each was recorded under
	)
	if strings.Contains(h, "/replays/device:kitchen") {
		t.Error("a satellite's rejection log has no turns to replay")
	}
	if e := get(t, newServer(journal.NewMemStore(), curation.NewMemStore(), fixtureBlobs(t), time.Now), "/replays"); !strings.Contains(e, "Nothing to replay yet.") {
		t.Error("an empty journal should say there is nothing to replay")
	}
}

// verifies SPEC §8
func TestReplayPageShowsEachRecordedTurnAndProvesTheLogReplays(t *testing.T) {
	h := get(t, newReplayServer(t, leadsWithTheCount()), "/replays/conv-1")
	missing(t, "Replay page", h,
		"play something by zeppelin", "just the first one",
		"I found three", " albums by that artist", // heard, then the tail nobody heard
		"Playing Led Zeppelin one.",
		"qwen3-32b@1", "sys@3", "tools@7",
		"replay() reads back all 13 events", // the reducer reads the whole log
		"matches the recorded prompt",       // the editor starts from what ran
		"Speak only by calling the speak tool.",
		`value="qwen3-32b@1"`,
		`hx-post="/replays/conv-1"`,
	)
	w := httptest.NewRecorder()
	newReplayServer(t, nil).routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/replays/conv-9", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("unknown conversation = %d, want 404", w.Code)
	}
}

// verifies SPEC §9.2
func TestReplayWithoutAModelSaysHowToConnectOne(t *testing.T) {
	h := get(t, newReplayServer(t, nil), "/replays/conv-1")
	missing(t, "Replay page", h, "OLLAMA_URL", "OLLAMA_MODEL")
	if !strings.Contains(h, `type="submit" disabled`) {
		t.Error("with no model to ask, the run button should be disabled")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/replays/conv-1", strings.NewReader("model=qwen3:32b&prompt=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	newReplayServer(t, nil).routes().ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("a run with no model = %d, want 503", w.Code)
	}
}

// verifies SPEC §9.2
func TestReplayRunsEveryTurnUnderTheEditedPromptAndDiffsIt(t *testing.T) {
	hh := leadsWithTheCount()
	s := newReplayServer(t, hh)
	h := runReplay(t, s, "conv-1", url.Values{"model": {"qwen3:32b"}, "prompt": {cutFirstPrompt}})

	if got := hh.built[len(hh.built)-1]; got != "qwen3:32b|"+cutFirstPrompt {
		t.Errorf("engine built with %q, want the edited model and prompt", got)
	}
	missing(t, "Replay result", h,
		"I found three albums. Want Led Zeppelin one?", // the new first answer
		"media_search",               // and the search it now makes first
		"speech and calls changed",   // turn 1
		"same",                       // turn 2 answered as recorded
		"1 of 2",                     // turns whose speech changed
		"qwen3:32b · sys@edited",     // what it ran under
		"offer the first, and stop.", // the prompt diff's added line
		`class="code-diff__line is-add"`,
	)
	if strings.Contains(h, "<html") {
		t.Error("an htmx run should swap in a fragment, not a page")
	}
}

// verifies SPEC §9.2
func TestAnEmptiedPromptIsRefusedOnThePage(t *testing.T) {
	hh := leadsWithTheCount()
	h := runReplay(t, newReplayServer(t, hh), "conv-1", url.Values{"model": {"qwen3:32b"}, "prompt": {"  \n"}})
	missing(t, "Replay result", h, "A re-run needs a model and a system prompt.", "play something by zeppelin")
	if len(hh.built) != 0 {
		t.Error("an empty prompt should not reach the model")
	}
}

// verifies SPEC §7
func TestAModelThatCannotAnswerStopsTheRunAndSaysWhy(t *testing.T) {
	hh := leadsWithTheCount()
	hh.fail = errors.New(`ollama chat: 404 Not Found: {"error":"model 'qwen3:70b' not found"}`)
	h := runReplay(t, newReplayServer(t, hh), "conv-1", url.Values{"model": {"qwen3:70b"}, "prompt": {ollama.DefaultPrompt}})
	missing(t, "Replay result", h, "Re-run stopped at #2", "model &#39;qwen3:70b&#39; not found")
}

// The endpoint answers 200, then the stream dies on the first turn: the run
// stops there and says why, rather than scoring a fragment as an answer.
//
// verifies SPEC §7
func TestAStreamThatDiesMidAnswerStopsTheRun(t *testing.T) {
	hh := leadsWithTheCount()
	hh.answers["play something by zeppelin"] = []sess.Action{
		sess.ToolCall{ID: "call_1", Tool: "media_search", Args: `{"query":"Led Zeppelin","limit":5}`},
		sess.TurnEnd{FinishReason: "error", Completion: `{"error":"llama runner process has terminated: signal: killed"}`},
	}
	h := runReplay(t, newReplayServer(t, hh), "conv-1", url.Values{"model": {"qwen3:32b"}, "prompt": {cutFirstPrompt}})
	missing(t, "Replay result", h, "Re-run stopped at #2", "llama runner process has terminated", "0 of 2")
	if strings.Contains(h, "speech and calls changed") {
		t.Error("a broken stream was scored as a changed answer")
	}
}

// verifies SPEC §9.2
func TestAThinkingModelDoesNotHoldTheReviewUI(t *testing.T) {
	hh := leadsWithTheCount()
	hh.gate = make(chan struct{})
	s := newReplayServer(t, hh)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runReplay(t, s, "conv-1", url.Values{"model": {"qwen3:32b"}, "prompt": {cutFirstPrompt}})
	}()
	served := make(chan struct{})
	go func() {
		defer close(served)
		get(t, s, "/curate/pairs")
	}()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Error("a model still thinking held the lock every page needs")
	}
	close(hh.gate)
	<-done
}

// verifies SPEC §9.2
func TestTheConversationLogOffersItsReplay(t *testing.T) {
	h := get(t, newReplayServer(t, nil), "/conversations/conv-1")
	missing(t, "conversation page", h, `href="/replays/conv-1"`)
}

// The real wiring: an unconfigured endpoint offers no engine, and a
// configured one builds an Ollama engine whose versions name the edit.
//
// verifies SPEC §9.2
func TestReplayAsksTheEndpointChorusdUses(t *testing.T) {
	if ollamaEngines("", "qwen3:32b") != nil || ollamaEngines("http://ollama.lan:11434", "") != nil {
		t.Error("a half-configured endpoint should offer no engine")
	}
	build := ollamaEngines("http://ollama.lan:11434", "qwen3:32b")
	_, recorded, err := build("qwen3:32b", ollama.DefaultPrompt)
	if err != nil {
		t.Fatal(err)
	}
	_, edited, err := build("qwen3:14b", cutFirstPrompt)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Model != "qwen3:14b" || edited.Prompt == recorded.Prompt || edited.ToolSchema != recorded.ToolSchema {
		t.Errorf("edited versions %+v against recorded %+v: want a new model and prompt, the same tools", edited, recorded)
	}
}
