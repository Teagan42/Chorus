// Package speakerid is the phase-1 speaker-embedding provider: an HTTP client
// for sidecars/speakerid, which serves TitaNet-L behind POST /v1/embed
// (SPEC §10, §14 item 4). It fills identity.Embedder.
//
// There is no standard speaker-embedding HTTP contract the way there is for
// speech, so this one is the repo's own and sidecars/speakerid/README.md is
// its other copy: raw device-format PCM in, a JSON vector out, with the
// dimension and model stated beside it so a sidecar that changed is refused
// here rather than scored as noise for weeks (ADR-0025).
package speakerid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/identity"
)

// DefaultModel is TitaNet-L, one of the two models SPEC §10 names, under
// the name the sidecar reports for its ONNX export (ADR-0029). Anything
// else is refused.
const DefaultModel = "nemo_en_titanet_large"

// DefaultDim is what that model emits. The sidecar declares it per
// response; this is only what the client expects to see.
const DefaultDim = 192

// DefaultTimeout bounds one request. An utterance is a few seconds of audio
// and the gate it feeds has a ~300 ms budget (SPEC §4.3), so a hung sidecar
// must not park the Listening child for a whole session. Not yet measured;
// the models tier logs what a call costs.
const DefaultTimeout = 10 * time.Second

// ContentType is the request body: bridge.SampleRate, bridge.BitsPerSample,
// mono, little-endian, no header. The sidecar accepts nothing else, so a
// WAV sent by mistake fails at the contract and not as a bad embedding.
const ContentType = "audio/pcm"

// errBody bounds how much of a failed response is quoted back. Enough for a
// validation message, not enough for a traceback in a log line.
const errBody = 1 << 10

// Config wires a Client. Everything that does I/O is injected, so `task test`
// stays hermetic (CONTRIBUTING).
type Config struct {
	// BaseURL is the endpoint root, e.g. http://127.0.0.1:8890.
	BaseURL string

	// Model defaults to DefaultModel; the sidecar must report the same.
	Model string

	// Dim defaults to DefaultDim; a response of any other width is refused.
	Dim int

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Client embeds one utterance per request.
type Client struct {
	cfg Config
	url string
}

var _ identity.Embedder = (*Client)(nil)

// New validates the wiring and applies defaults.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("speakerid: base url is required")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Dim == 0 {
		cfg.Dim = DefaultDim
	}
	if cfg.Dim < 0 {
		return nil, fmt.Errorf("speakerid: dim %d is not positive", cfg.Dim)
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{cfg: cfg, url: strings.TrimSuffix(cfg.BaseURL, "/") + "/v1/embed"}, nil
}

// Model is the checkpoint this client is built for (identity.Embedder).
func (c *Client) Model() string { return c.cfg.Model }

// Dim is the width this client is built for (identity.Embedder).
func (c *Client) Dim() int { return c.cfg.Dim }

// response is one POST /v1/embed reply.
type response struct {
	Embedding []float32 `json:"embedding"`
	Dim       int       `json:"dim"`
	Model     string    `json:"model"`
}

// Embed posts the utterance and checks the reply against what the client was
// built for. The checks are the contract: a sidecar upgrade that changed the
// model would otherwise score every household member as a stranger with no
// error anywhere.
func (c *Client) Embed(ctx context.Context, pcm []byte) ([]float32, error) {
	// Neither is worth a round trip: the sidecar answers 400 to both, and an
	// odd byte count means every later sample is a byte out of phase.
	if len(pcm) == 0 {
		return nil, errors.New("speakerid: no audio to embed")
	}
	if len(pcm)%(bridge.BitsPerSample/8) != 0 {
		return nil, fmt.Errorf("speakerid: %d bytes is not whole %d-bit samples", len(pcm), bridge.BitsPerSample)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(pcm))
	if err != nil {
		return nil, fmt.Errorf("speakerid: build request: %w", err)
	}
	req.Header.Set("Content-Type", ContentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("speakerid embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Quoted because the status alone does not say whether the audio was
		// too short, the wrong format, or the model failed to load.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return nil, fmt.Errorf("speakerid embed: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var r response
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, fmt.Errorf("speakerid embed: decode reply: %w", err)
	}
	switch {
	case r.Dim != len(r.Embedding):
		return nil, fmt.Errorf("speakerid embed: sidecar declared %d dims and sent %d", r.Dim, len(r.Embedding))
	case r.Dim != c.cfg.Dim:
		return nil, fmt.Errorf("speakerid embed: sidecar embeds at %d dims, client is built for %d", r.Dim, c.cfg.Dim)
	case r.Model != c.cfg.Model:
		return nil, fmt.Errorf("speakerid embed: sidecar serves %q, client is built for %q", r.Model, c.cfg.Model)
	}
	return r.Embedding, nil
}
