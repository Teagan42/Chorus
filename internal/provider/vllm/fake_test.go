package vllm

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

	"github.com/teagan42/chorus/internal/session"
)

// fakeVLLM is a vLLM OpenAI server in process: it answers each POST to
// /v1/chat/completions with the next of its streams, the last one again once
// they run out, flushing event by event as the real server does, and keeps
// every request it was sent.
type fakeVLLM struct {
	mu       sync.Mutex
	streams  []string
	status   int
	requests []served
}

// served is one request as the server saw it.
type served struct {
	path   string
	header http.Header
	body   []byte
}

func newFake(t *testing.T, streams ...string) (*fakeVLLM, *httptest.Server) {
	t.Helper()
	f := &fakeVLLM{streams: streams}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeVLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, served{path: r.URL.Path, header: r.Header.Clone(), body: body})
	n := len(f.requests)
	status := f.status
	stream := ""
	if len(f.streams) > 0 {
		stream = f.streams[min(n, len(f.streams))-1]
	}
	f.mu.Unlock()

	if status != 0 {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, stream)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	flusher, _ := w.(http.Flusher)
	for event := range strings.SplitAfterSeq(stream, "\n\n") {
		if _, err := io.WriteString(w, event); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// last is the most recent request.
func (f *fakeVLLM) last(t *testing.T) served {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("the server was sent nothing")
	}
	return f.requests[len(f.requests)-1]
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return string(b)
}

// engineAt wires an Engine to a server.
func engineAt(t *testing.T, srv *httptest.Server, cfg Config) *Engine {
	t.Helper()
	cfg.BaseURL = srv.URL
	if cfg.Model == "" {
		cfg.Model = "Qwen/Qwen3-32B-AWQ"
	}
	cfg.HTTP = srv.Client()
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return e
}

func drain(t *testing.T, ch <-chan session.Action) []session.Action {
	t.Helper()
	var got []session.Action
	for a := range ch {
		got = append(got, a)
	}
	return got
}

// collect decodes r and drains concurrently, as the session does: decode
// emits per fragment, and an unread channel would deadlock.
func collect(t *testing.T, ctx context.Context, r io.Reader) ([]session.Action, error) {
	t.Helper()
	out := make(chan session.Action)
	done := make(chan []session.Action, 1)
	go func() {
		var got []session.Action
		for a := range out {
			got = append(got, a)
		}
		done <- got
	}()
	err := decoder{}.decode(ctx, r, out)
	close(out)
	return <-done, err
}

func speechOf(acts []session.Action) []session.SpeechDelta {
	var out []session.SpeechDelta
	for _, a := range acts {
		if s, ok := a.(session.SpeechDelta); ok {
			out = append(out, s)
		}
	}
	return out
}

func callsOf(acts []session.Action) []session.ToolCall {
	var out []session.ToolCall
	for _, a := range acts {
		if c, ok := a.(session.ToolCall); ok {
			out = append(out, c)
		}
	}
	return out
}

func endOf(t *testing.T, acts []session.Action) session.TurnEnd {
	t.Helper()
	if len(acts) == 0 {
		t.Fatal("the turn produced nothing")
	}
	end, ok := acts[len(acts)-1].(session.TurnEnd)
	if !ok {
		t.Fatalf("last action is %T, want TurnEnd: %v", acts[len(acts)-1], kinds(acts))
	}
	return end
}

// kinds names each action; speech carries its call and whether it is last.
func kinds(acts []session.Action) []string {
	out := make([]string, len(acts))
	for i, a := range acts {
		switch v := a.(type) {
		case session.SpeechDelta:
			out[i] = "speech"
			if v.Last {
				out[i] = "speech:last"
			}
		case session.ToolCall:
			out[i] = "call:" + v.Tool
		case session.TurnEnd:
			out[i] = "end:" + v.FinishReason
		}
	}
	return out
}

// said joins the words of one utterance.
func said(sp []session.SpeechDelta, callID string) string {
	var b strings.Builder
	for _, d := range sp {
		if d.CallID == callID {
			b.WriteString(d.Text)
		}
	}
	return b.String()
}
