package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/session"
)

// onVLLM is a complete environment that runs the turn engine on vLLM, with
// Ollama kept for embeddings only.
func onVLLM() map[string]string {
	env := complete()
	env[llmProviderEnv] = "vllm"
	env[vllmURLEnv] = "http://10.0.0.20:8000"
	env[vllmModelEnv] = "Qwen/Qwen3-32B-AWQ"
	env[vllmKeyEnv] = "sk-household-7f3a"
	delete(env, ollamaModelEnv)
	return env
}

// A house that says nothing about its LLM provider runs Ollama, as it did
// before vLLM could be chosen.
//
// verifies SPEC §10, §13
func TestTheTurnEngineIsOllamaUnlessVLLMIsChosen(t *testing.T) {
	p, err := buildProviders(configFromEnv(lookup(complete())), nil, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if _, ok := p.engine.(*ollama.Engine); !ok {
		t.Errorf("engine is %T, want Ollama's", p.engine)
	}
	env := complete()
	env[llmProviderEnv] = "ollama"
	if err := configFromEnv(lookup(env)).validate(); err != nil {
		t.Errorf("naming ollama explicitly: %v", err)
	}
}

// CHORUS_LLM_PROVIDER=vllm runs the turn engine and the summaries on vLLM,
// recorded under the model vLLM serves; Ollama's model is not needed, and
// the embedding model stays on Ollama, which vLLM does not serve beside a
// chat model (ADR-0077).
//
// verifies SPEC §10, §13
func TestVLLMRunsTheTurnsAndOllamaKeepsTheEmbeddings(t *testing.T) {
	cfg := configFromEnv(lookup(onVLLM()))
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	p, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	eng, ok := p.engine.(*vllm.Engine)
	if !ok {
		t.Fatalf("engine is %T, want vLLM's", p.engine)
	}
	if s, ok := p.summarizer.(*vllm.Engine); !ok || s != eng {
		t.Errorf("summarizer is %T, want the turn engine itself", p.summarizer)
	}
	if p.versions.Model != "Qwen/Qwen3-32B-AWQ" || p.versions.Prompt == "" || p.versions.ToolSchema == "" {
		t.Errorf("versions = %+v, want vLLM's model", p.versions)
	}
	if p.embedder == nil || p.embedder.EmbedModel() != "nomic-embed-text" {
		t.Errorf("embedder = %v, want nomic-embed-text on Ollama", p.embedder)
	}
}

// Validation names what the chosen provider needs and nothing the other
// one does; an embedding model with no Ollama to serve it, a provider
// nobody knows and a thinking switch that is not a boolean are refused at
// startup rather than found on the first turn (SPEC §13).
//
// verifies SPEC §13
func TestValidateNamesWhatTheChosenProviderNeeds(t *testing.T) {
	bare := map[string]string{llmProviderEnv: "vllm", blobDirEnv: "/var/lib/chorus/blobs", kokoroURLEnv: "http://kokoro:8880", sttURLEnv: "http://speaches:8000"}
	err := configFromEnv(lookup(bare)).validate()
	if err == nil {
		t.Fatal("vllm without its endpoint validated")
	}
	for _, name := range []string{vllmURLEnv, vllmModelEnv} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	for _, name := range []string{ollamaURLEnv, ollamaModelEnv, vllmKeyEnv} {
		if strings.Contains(err.Error(), name) {
			t.Errorf("error demands %s, which vllm without embeddings does not need: %v", name, err)
		}
	}

	for name, c := range map[string]struct {
		set   map[string]string
		unset []string
		says  string
	}{
		"embeddings with no ollama": {unset: []string{ollamaURLEnv}, says: ollamaURLEnv},
		"an unknown provider":       {set: map[string]string{llmProviderEnv: "llamacpp"}, says: `"llamacpp"`},
		"thinking that is not bool": {set: map[string]string{vllmThinkingEnv: "sometimes"}, says: vllmThinkingEnv},
	} {
		env := onVLLM()
		for k, v := range c.set {
			env[k] = v
		}
		for _, k := range c.unset {
			delete(env, k)
		}
		if err := configFromEnv(lookup(env)).validate(); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: err = %v, want it to name %s", name, err, c.says)
		}
	}
	env := onVLLM()
	delete(env, ollamaURLEnv)
	delete(env, ollamaEmbedEnv)
	if err := configFromEnv(lookup(env)).validate(); err != nil {
		t.Errorf("vllm with no ollama at all: %v", err)
	}
}

// Readiness asks vLLM's own health route, which answers without the API
// key, when vLLM is the turn engine (ADR-0068).
//
// verifies SPEC §13
func TestReadinessProbesTheChosenLLM(t *testing.T) {
	var asked []string
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		asked = append(asked, r.URL.String())
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}
	for _, c := range opsChecks(configFromEnv(lookup(onVLLM())), client, func(context.Context) error { return nil }) {
		if c.Name == "llm" {
			if err := c.Probe(context.Background()); err != nil {
				t.Fatalf("probe: %v", err)
			}
		}
	}
	if !slices.Equal(asked, []string{"http://10.0.0.20:8000/health"}) {
		t.Errorf("the llm probe asked %q, want vLLM's /health", asked)
	}
}

// recordedVLLM answers each /v1/chat/completions with the next stream, as
// vLLM streams it, and keeps each request it was sent.
type recordedVLLM struct {
	mu      sync.Mutex
	streams [][]byte
	sent    [][]byte
}

func (v *recordedVLLM) RoundTrip(r *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(r.Body)
	v.mu.Lock()
	defer v.mu.Unlock()
	if r.URL.Path != "/v1/chat/completions" || len(v.sent) == len(v.streams) {
		return nil, errors.New("vllm asked more than the test recorded: " + r.URL.String())
	}
	v.sent = append(v.sent, body)
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Request: r,
		Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body:   io.NopCloser(bytes.NewReader(v.streams[len(v.sent)-1])),
	}, nil
}

func (v *recordedVLLM) request(t *testing.T, i int) []byte {
	t.Helper()
	v.mu.Lock()
	defer v.mu.Unlock()
	if i >= len(v.sent) {
		t.Fatalf("vllm was asked %d times, want at least %d", len(v.sent), i+1)
	}
	return v.sent[i]
}

// Alan asks the kitchen to turn off the kitchen and porch lights, with
// vLLM as the turn engine. Its stream says what it is doing while both
// lights are switched, it is asked again with both results paired to
// their calls by id, says they are off and ends the session: the whole
// daemon, device to log, over the vLLM engine (SPEC §4.1, §12).
//
// verifies SPEC §4.1, §4.4, §12
func TestAlanTurnsOffTheLightsWithVLLMAsTheTurnEngine(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "provider", "vllm", "testdata", name))
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		return b
	}
	rec := &recordedVLLM{streams: [][]byte{read("speak_and_two_lights.sse"), read("done_and_end.sse")}}
	eng, err := vllm.New(vllm.Config{BaseURL: "http://10.0.0.20:8000", Model: "Qwen/Qwen3-32B-AWQ", HTTP: &http.Client{Transport: rec}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	var mu sync.Mutex
	var switched []string
	lights := session.ToolFunc(func(_ context.Context, args string) (string, error) {
		var a struct {
			EntityID string `json:"entity_id"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "", err
		}
		mu.Lock()
		switched = append(switched, a.EntityID)
		mu.Unlock()
		return `{"changed":["` + a.EntityID + `"]}`, nil
	})
	r := newRig(t, inventory(), func(d *deps) {
		d.engine, d.summarizer, d.versions = eng, nil, eng.Versions()
		d.tools = map[string]session.Tool{"ha_call_service": lights}
		d.Timers = wallTimers{}
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn off the kitchen and porch lights", alan))

	await(t, "both answers to be heard", func() bool {
		dev.PlayAll(t)
		return len(r.store.ofKind(journal.KindSpeechSpoken)) >= 2
	})
	var heard []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		heard = append(heard, e.Fields["text"])
	}
	if want := []string{"Turning off the kitchen and porch lights.", "Done, both are off."}; !slices.Equal(heard, want) {
		t.Errorf("speech_spoken = %q, want %q", heard, want)
	}
	closed := r.store.awaitKind(t, journal.KindSessionClosed, 1)
	if closed.Fields["reason"] != "model_ended" {
		t.Errorf("session_closed = %v, want the model to have ended it", closed.Fields)
	}
	mu.Lock()
	// The two calls run side by side, so either may land first.
	slices.Sort(switched)
	if !slices.Equal(switched, []string{"light.kitchen", "light.porch"}) {
		t.Errorf("lights switched = %v", switched)
	}
	mu.Unlock()
	for _, e := range r.store.ofKind(journal.KindModelCompleted) {
		if e.Versions.Model != "Qwen/Qwen3-32B-AWQ" || e.Fields["finish_reason"] != "tool_calls" {
			t.Errorf("model_completed = %+v", e)
		}
	}

	var followUp struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.request(t, 1), &followUp); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	for _, m := range followUp.Messages {
		if m.Role == "tool" {
			results[m.ToolCallID] = m.Content
		}
	}
	if results["chatcmpl-tool-a83f61c09e2d47d5"] != `{"changed":["light.kitchen"]}` || results["chatcmpl-tool-0c9b7e5f3a6148e1"] != `{"changed":["light.porch"]}` {
		t.Errorf("the follow-up's results by call id = %v", results)
	}
}
