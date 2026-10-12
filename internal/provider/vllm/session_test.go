package vllm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/session"
)

// These run the real supervisor over the real engine, with only the server,
// the speaker and the clock faked: the seam where a vLLM that is down,
// broken or hung becomes what the household hears and what the log keeps
// (SPEC §7, ADR-0051).

var breakfast = time.Date(2026, 10, 10, 7, 42, 0, 0, time.UTC)

// deadline hands the model's deadline to the test, which fires it on purpose.
// Every other timer, the silence backstop included, never fires.
type deadline struct {
	model time.Duration
	fire  chan time.Time
}

func newDeadline(model time.Duration) *deadline {
	return &deadline{model: model, fire: make(chan time.Time)}
}

func (d *deadline) Now() time.Time { return breakfast }

func (d *deadline) After(wait time.Duration) <-chan time.Time {
	if wait != d.model {
		return make(chan time.Time)
	}
	return d.fire
}

// expire fires the deadline until stop closes: the watchdog rearms it each
// time the model acts, and a firing that races an action is let go.
func (d *deadline) expire(stop <-chan struct{}) {
	for {
		select {
		case d.fire <- breakfast.Add(d.model):
		case <-stop:
			return
		}
	}
}

// kitchen plays every utterance whole and keeps what it played. started
// closes when the first utterance begins to play.
type kitchen struct {
	mu      sync.Mutex
	played  []string
	started chan struct{}
	once    sync.Once
}

func (k *kitchen) Open(context.Context, string) (session.Stream, error) {
	k.once.Do(func() { close(k.started) })
	return &kitchenStream{k: k}, nil
}

func (k *kitchen) heard() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.played...)
}

type kitchenStream struct {
	k    *kitchen
	text string
}

func (s *kitchenStream) Write(text string) error { s.text += text; return nil }

func (s *kitchenStream) Close() session.Playback {
	s.k.mu.Lock()
	s.k.played = append(s.k.played, s.text)
	s.k.mu.Unlock()
	return session.Playback{Spoken: s.text, Frames: int64(len(s.text)) * 160, AudioRef: "blob://tts/kitchen"}
}

type household struct {
	store   *journal.MemStore
	kitchen *kitchen
	clock   *deadline
	sess    *session.Session
}

func newHousehold(t *testing.T, h http.Handler) *household {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	eng, err := vllm.New(vllm.Config{BaseURL: srv.URL, Model: "Qwen/Qwen3-32B-AWQ", HTTP: srv.Client()})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	hh := &household{store: journal.NewMemStore(), kitchen: &kitchen{started: make(chan struct{})}, clock: newDeadline(session.DefaultModelTimeout)}
	sup, err := session.New(session.Config{
		Journal: journal.New(hh.store, hh.clock, eng.Versions()),
		Store:   hh.store, Clock: hh.clock, Timers: hh.clock,
		Engine: eng, Speaker: hh.kitchen,
	})
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	if hh.sess, err = sup.Open(t.Context(), session.Wake{Satellite: "kitchen", PersonID: "teagan"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	return hh
}

func (h *household) ask(text string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- h.sess.Heard(context.Background(), session.Transcript{Text: text, SpeakerID: "teagan", AudioRef: "blob://mic/1"})
	}()
	return out
}

func (h *household) events(t *testing.T, kind journal.Kind) []journal.Event {
	t.Helper()
	events, err := h.store.Events(context.Background(), h.sess.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var found []journal.Event
	for _, e := range events {
		if e.Kind == kind {
			found = append(found, e)
		}
	}
	return found
}

func (h *household) answered(t *testing.T, errc <-chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Heard = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never finished")
	}
}

func (h *household) failed(t *testing.T, errc <-chan error) journal.Event {
	t.Helper()
	h.answered(t, errc)
	found := h.events(t, journal.KindModelFailed)
	if len(found) != 1 {
		t.Fatalf("%d model_failed, want 1", len(found))
	}
	return found[0]
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// streams answers each ask with the next stream.
func streams(bodies ...string) http.HandlerFunc {
	var mu sync.Mutex
	n := 0
	return func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		body := bodies[min(n, len(bodies)-1)]
		n++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
}

// Teagan asks whether the garage door is closed. The kitchen says it is
// checking while the cover is read, the model is asked again with the
// door's state, and says so: the whole turn, over vLLM, as the log keeps it.
//
// verifies SPEC §4.1, §4.4, §8
func TestATurnOverVLLMIsSpokenDispatchedAndKept(t *testing.T) {
	h := newHousehold(t, streams(
		readFixture(t, "check_the_garage.sse"),
		readFixture(t, "done_and_end.sse"),
	))
	h.answered(t, h.ask("is the garage door closed"))

	want := []string{"Let me check the garage door.", "Done, both are off."}
	if got := h.kitchen.heard(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the kitchen played %q, want %q", got, want)
	}
	called := h.events(t, journal.KindToolCalled)
	var tools []string
	for _, e := range called {
		tools = append(tools, e.Fields["tool"])
	}
	if strings.Join(tools, ",") != "speak,ha_get_state,speak,end_session" {
		t.Errorf("tool_called = %v", tools)
	}
	completed := h.events(t, journal.KindModelCompleted)
	if len(completed) != 2 || completed[0].Fields["finish_reason"] != "tool_calls" || completed[0].Versions.Model != "Qwen/Qwen3-32B-AWQ" {
		t.Errorf("model_completed = %+v", completed)
	}
}

// vLLM refuses the ask before streaming anything and says why in its body.
// The kitchen apologises, and the log keeps vLLM's own words.
//
// verifies SPEC §7
func TestVLLMRefusingTheAskIsSaidAndKept(t *testing.T) {
	h := newHousehold(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"This model's maximum context length is 32768 tokens. However, you requested 33012 tokens.","type":"BadRequestError","param":null,"code":400}}`)
	}))

	failed := h.failed(t, h.ask("turn off the kitchen lights"))
	if failed.Fields["reason"] != "unavailable" || !strings.Contains(failed.Fields["error"], "maximum context length") {
		t.Errorf("model_failed = %v, want vLLM's refusal", failed.Fields)
	}
	if got := h.kitchen.heard(); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("the kitchen played %q, want the canned line", got)
	}
}

// The model starts saying it is checking the garage door and the server
// fails in-band. What was said stays said; the apology follows; vLLM's
// stream error is the reason kept.
//
// verifies SPEC §7
func TestVLLMsStreamBreakingIsSaidAfterWhatWasHeard(t *testing.T) {
	h := newHousehold(t, streams(readFixture(t, "error_mid_stream.sse")))

	failed := h.failed(t, h.ask("is the garage door shut"))
	if failed.Fields["reason"] != "failed" || !strings.Contains(failed.Fields["error"], "EngineCore") {
		t.Errorf("model_failed = %v, want the broken stream", failed.Fields)
	}
	want := []string{"Let me check the garage door", session.DefaultCanned.Model}
	if got := h.kitchen.heard(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the kitchen played %q, want %q", got, want)
	}
}

// The stream stalls mid-speech: the model has said "Let me check the gar"
// and nothing follows. The client has no timeout, on purpose, so the
// session's deadline gives up on it: the request is cancelled, what was
// said stays said, the kitchen apologises, and the session hears again.
//
// verifies SPEC §4.5, §7
func TestAVLLMThatStallsMidSpeechIsGivenUpOnAtTheDeadline(t *testing.T) {
	stream := readFixture(t, "error_mid_stream.sse")
	// Up to the speak call's first words and no further: the deadline fires
	// once they are playing, so they are all that can have been said.
	stream = stream[:strings.LastIndex(stream[:strings.Index(stream, "age door")], "data: ")]
	h := newHousehold(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stream)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))

	errc := h.ask("is the garage door shut")
	// Only once the words are playing, so the deadline is the one the
	// model's last action rearmed.
	<-h.kitchen.started
	stop := make(chan struct{})
	defer close(stop)
	go h.clock.expire(stop)
	failed := h.failed(t, errc)
	if failed.Fields["reason"] != "timed_out" {
		t.Errorf("model_failed = %v, want timed_out", failed.Fields)
	}
	want := []string{"Let me check the gar", session.DefaultCanned.Model}
	if got := h.kitchen.heard(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the kitchen played %q, want %q", got, want)
	}
	select {
	case <-h.sess.Done():
		t.Error("the session closed; it should be listening again")
	default:
	}
}
