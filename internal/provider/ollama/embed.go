package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/teaganglenn/chorus/internal/memory"
)

// maxEmbedBody bounds one /api/embed response: a batch of vectors is a few
// hundred kilobytes of JSON numbers, not the megabyte of one chat line.
const maxEmbedBody = 16 << 20

// EmbedConfig wires an Embedder.
type EmbedConfig struct {
	// BaseURL is the endpoint root, the turn engine's own as a rule.
	BaseURL string
	// Model is an embedding model the endpoint has pulled, such as
	// nomic-embed-text.
	Model string

	// HTTP defaults to a plain client; the recall's context bounds a call.
	HTTP *http.Client

	// KeepAlive defaults to DefaultKeepAlive, for the reason the turn model
	// stays resident: an embedding model loaded cold misses every turn's
	// bound until it is warm.
	KeepAlive string
}

// Embedder is Ollama's /api/embed: what recall ranks memories and past
// conversations with when a person has more than a turn is told (ADR-0044).
type Embedder struct {
	cfg EmbedConfig
	url string
}

var _ memory.Embedder = (*Embedder)(nil)

// NewEmbedder validates the wiring without touching the network.
func NewEmbedder(cfg EmbedConfig) (*Embedder, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("ollama: embed: base url is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("ollama: embed: model is required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.KeepAlive == "" {
		cfg.KeepAlive = DefaultKeepAlive
	}
	return &Embedder{cfg: cfg, url: strings.TrimSuffix(cfg.BaseURL, "/") + "/api/embed"}, nil
}

// EmbedModel is the model the vectors come from.
func (e *Embedder) EmbedModel() string { return e.cfg.Model }

type embedRequest struct {
	Model     string   `json:"model"`
	Input     []string `json:"input"`
	KeepAlive string   `json:"keep_alive,omitempty"`
}

type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
	Error      string      `json:"error"`
}

// Embed returns one vector per text, in order, from one request.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.cfg.Model, Input: texts, KeepAlive: e.cfg.KeepAlive})
	if err != nil {
		return nil, fmt.Errorf("ollama: encode embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama: build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return nil, fmt.Errorf("ollama embed: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var r embedResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxEmbedBody)).Decode(&r); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w", err)
	}
	if r.Error != "" {
		return nil, errors.New("ollama embed: " + r.Error)
	}
	if len(r.Embeddings) != len(texts) {
		return nil, fmt.Errorf("ollama embed: %d vectors for %d texts", len(r.Embeddings), len(texts))
	}
	return r.Embeddings, nil
}
