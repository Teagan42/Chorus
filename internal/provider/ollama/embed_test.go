package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func embedderOn(t *testing.T, rt *roundTrip) *Embedder {
	t.Helper()
	e, err := NewEmbedder(EmbedConfig{
		BaseURL: "http://ollama.lan:11434/", Model: "nomic-embed-text", HTTP: &http.Client{Transport: rt},
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	return e
}

// Recall embeds Teagan's words and a memory in one request, kept resident
// as the turn model is, and gets one vector back for each, in order.
//
// verifies SPEC §5
func TestEmbedSendsEveryTextInOneRequest(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "embed.json")}
	got, err := embedderOn(t, rt).Embed(context.Background(), []string{
		"what's the code for the garage", "The garage door code is 4512.",
	})
	if err != nil {
		t.Fatalf("embed: %v", err)
	}
	want := [][]float32{{0.0213, -0.0418, 0.0592, 0.0077}, {0.0198, -0.0377, 0.0614, -0.0123}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vectors = %v, want %v", got, want)
	}
	var req map[string]any
	if err := json.Unmarshal(rt.reqBody, &req); err != nil {
		t.Fatalf("request: %v", err)
	}
	wantReq := map[string]any{
		"model":      "nomic-embed-text",
		"input":      []any{"what's the code for the garage", "The garage door code is 4512."},
		"keep_alive": DefaultKeepAlive,
	}
	if !reflect.DeepEqual(req, wantReq) {
		t.Errorf("request = %v, want %v", req, wantReq)
	}
}

// Every way the endpoint can fail is an error recall falls back on, never a
// vector it ranks with.
//
// verifies SPEC §5, §7
func TestEmbedFailuresAreErrors(t *testing.T) {
	cases := []struct {
		why  string
		rt   *roundTrip
		want string
	}{
		{"the model is not pulled", &roundTrip{status: http.StatusNotFound, body: `{"error":"model \"nomic-embed-text\" not found, try pulling it first"}`}, "404 Not Found"},
		{"the endpoint is down", &roundTrip{err: errors.New("connection refused")}, "connection refused"},
		{"an error in the body", &roundTrip{body: `{"error":"input length exceeds the context length"}`}, "context length"},
		{"one vector for two texts", &roundTrip{body: `{"model":"nomic-embed-text","embeddings":[[0.1,0.2]]}`}, "1 vectors for 2 texts"},
		{"not JSON", &roundTrip{body: `<html>Bad Gateway</html>`}, "decode"},
	}
	for _, c := range cases {
		_, err := embedderOn(t, c.rt).Embed(context.Background(), []string{"is the oven on", "The oven runs hot."})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one mentioning %q", c.why, err, c.want)
		}
	}
}

// An embedder needs to know where and which model.
//
// verifies SPEC §13
func TestNewEmbedderNeedsAnEndpointAndAModel(t *testing.T) {
	if _, err := NewEmbedder(EmbedConfig{Model: "nomic-embed-text"}); err == nil {
		t.Error("no url accepted")
	}
	if _, err := NewEmbedder(EmbedConfig{BaseURL: "http://ollama.lan:11434"}); err == nil {
		t.Error("no model accepted")
	}
	e, err := NewEmbedder(EmbedConfig{BaseURL: "http://ollama.lan:11434/", Model: "nomic-embed-text"})
	if err != nil || e.url != "http://ollama.lan:11434/api/embed" || e.EmbedModel() != "nomic-embed-text" {
		t.Errorf("embedder = %+v, %v", e, err)
	}
}
