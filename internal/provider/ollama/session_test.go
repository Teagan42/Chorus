package ollama_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/session"
)

// These run the real supervisor over the real engine, with only the wire,
// the speaker and the clock faked: the seam where an Ollama that is down,
// broken or hung becomes what the household hears and what the log keeps
// (SPEC §7, ADR-0051).

var breakfast = time.Date(2026, 10, 10, 7, 42, 0, 0, time.UTC)

// wire is the HTTP transport in front of Ollama.
type wire func(*http.Request) (*http.Response, error)

func (w wire) RoundTrip(r *http.Request) (*http.Response, error) { return w(r) }

// deadline hands the model's deadline to the test, which fires it on purpose.
// Every other timer, the silence backstop included, never fires.
type deadline struct {
	now   time.Time
	model time.Duration
	fire  chan time.Time
	armed chan struct{}
	once  sync.Once
}

func newDeadline(model time.Duration) *deadline {
	return &deadline{now: breakfast, model: model, fire: make(chan time.Time, 1), armed: make(chan struct{})}
}

func (d *deadline) Now() time.Time { return d.now }

func (d *deadline) After(wait time.Duration) <-chan time.Time {
	if wait != d.model {
		return make(chan time.Time)
	}
	d.once.Do(func() { close(d.armed) })
	return d.fire
}

// kitchen plays every utterance whole and keeps what it played.
type kitchen struct {
	mu     sync.Mutex
	played []string
}

func (k *kitchen) Open(context.Context, string) (session.Stream, error) {
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

func newHousehold(t *testing.T, w wire) *household {
	t.Helper()
	eng, err := ollama.New(ollama.Config{
		BaseURL: "http://10.0.0.20:11434", Model: "qwen3:14b",
		HTTP: &http.Client{Transport: w},
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	h := &household{store: journal.NewMemStore(), kitchen: &kitchen{}, clock: newDeadline(session.DefaultModelTimeout)}
	sup, err := session.New(session.Config{
		Journal: journal.New(h.store, h.clock, eng.Versions()),
		Store:   h.store, Clock: h.clock, Timers: h.clock,
		Engine: eng, Speaker: h.kitchen,
	})
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	if h.sess, err = sup.Open(t.Context(), session.Wake{Satellite: "kitchen", PersonID: "teagan"}); err != nil {
		t.Fatalf("open: %v", err)
	}
	return h
}

func (h *household) ask(text string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- h.sess.Heard(context.Background(), session.Transcript{Text: text, SpeakerID: "teagan", AudioRef: "blob://mic/1"})
	}()
	return out
}

func (h *household) failed(t *testing.T, errc <-chan error) journal.Event {
	t.Helper()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Heard = %v, want the failure said and journalled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn never finished")
	}
	events, err := h.store.Events(context.Background(), h.sess.ConversationID())
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var found []journal.Event
	for _, e := range events {
		if e.Kind == journal.KindModelFailed {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d model_failed, want 1", len(found))
	}
	return found[0]
}

// Ollama runs out of memory loading the model and says so in its body. The
// kitchen apologises, and the log keeps Ollama's own words.
//
// verifies SPEC §7
func TestOllamaRefusingTheAskIsSaidAndKept(t *testing.T) {
	h := newHousehold(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error",
			Body: io.NopCloser(strings.NewReader(`{"error":"model requires more system memory (21.3 GiB) than is available (15.2 GiB)"}`)),
		}, nil
	})

	failed := h.failed(t, h.ask("turn off the kitchen lights"))
	if failed.Fields["reason"] != "unavailable" || !strings.Contains(failed.Fields["error"], "more system memory") {
		t.Errorf("model_failed = %v, want Ollama's refusal", failed.Fields)
	}
	if failed.Versions.Model != "qwen3:14b" {
		t.Errorf("versions = %+v, want the model that failed", failed.Versions)
	}
	if got := h.kitchen.heard(); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("the kitchen played %q, want the canned line", got)
	}
}

// The stream starts, the model says it is checking, and the connection
// drops. What was said stays said; the apology follows; Ollama's stream
// error is the reason kept.
//
// verifies SPEC §7
func TestOllamasStreamBreakingIsSaidAfterWhatWasHeard(t *testing.T) {
	const checking = `{"model":"qwen3:14b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_v3d4os9t","function":{"index":0,"name":"speak","arguments":{"mode":"queue","text":"Let me check the garage door."}}}]},"done":false}` + "\n"
	h := newHousehold(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK",
			Body: io.NopCloser(io.MultiReader(strings.NewReader(checking), brokenPipe{})),
		}, nil
	})

	failed := h.failed(t, h.ask("is the garage door shut"))
	if failed.Fields["reason"] != "failed" || !strings.Contains(failed.Fields["error"], "connection reset by peer") {
		t.Errorf("model_failed = %v, want the broken stream", failed.Fields)
	}
	want := []string{"Let me check the garage door.", session.DefaultCanned.Model}
	if got := h.kitchen.heard(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the kitchen played %q, want %q", got, want)
	}
}

type brokenPipe struct{}

func (brokenPipe) Read([]byte) (int, error) {
	return 0, errors.New("read tcp 10.0.0.5:51334->10.0.0.20:11434: read: connection reset by peer")
}

// Ollama accepts the ask and answers nothing at all. The client has no
// timeout, on purpose, so only the session's deadline ends it: the request
// is cancelled, the kitchen apologises, and the session hears again.
//
// verifies SPEC §4.5, §7
func TestAHungOllamaIsGivenUpOnAtTheDeadline(t *testing.T) {
	h := newHousehold(t, func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})

	errc := h.ask("what's on the calendar today")
	<-h.clock.armed
	h.clock.fire <- breakfast.Add(session.DefaultModelTimeout)
	failed := h.failed(t, errc)
	if failed.Fields["reason"] != "timed_out" {
		t.Errorf("model_failed = %v, want timed_out", failed.Fields)
	}
	if got := h.kitchen.heard(); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("the kitchen played %q, want the canned line", got)
	}
	select {
	case <-h.sess.Done():
		t.Error("the session closed; it should be listening again")
	default:
	}
}
