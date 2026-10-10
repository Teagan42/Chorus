// Package smartturn is the semantic-endpointing provider: an HTTP client for
// sidecars/smartturn, which serves Smart Turn v3.2 behind POST /v1/turn
// (SPEC §4.5, §10). It fills listen.Judge.
//
// The contract is the repo's own, and sidecars/smartturn/README.md is its
// other copy: the turn so far as raw device-format PCM in, a verdict out,
// with the checkpoint named beside it so a sidecar that changed is refused
// here rather than trusted with every turn (ADR-0036).
package smartturn

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/listen"
)

// DefaultModel is the checkpoint the sidecar pins (ADR-0036). Anything
// else is refused.
const DefaultModel = "smart-turn-v3.2-cpu"

// DefaultTimeout bounds one request. The endpointer cancels an ask sooner,
// when the pause it was about ends; this only catches a hung sidecar.
const DefaultTimeout = 5 * time.Second

// ContentType is the request body: bridge.SampleRate, bridge.BitsPerSample,
// mono, little-endian, no header.
const ContentType = "audio/pcm"

// errBody bounds how much of a failed response is quoted back.
const errBody = 1 << 10

// Config wires a Client. Everything that does I/O is injected, so `task test`
// stays hermetic (CONTRIBUTING §1).
type Config struct {
	// BaseURL is the endpoint root, e.g. http://127.0.0.1:8891.
	BaseURL string

	// Model defaults to DefaultModel; the sidecar must report the same.
	Model string

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Client asks whether a turn is over, one request per pause.
type Client struct {
	cfg Config
	url string
}

var _ listen.Judge = (*Client)(nil)

// New validates the wiring and applies defaults.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("smartturn: base url is required")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Client{cfg: cfg, url: strings.TrimSuffix(cfg.BaseURL, "/") + "/v1/turn"}, nil
}

// Model is the checkpoint this client is built for.
func (c *Client) Model() string { return c.cfg.Model }

// Verdict is one POST /v1/turn reply.
type Verdict struct {
	Complete    bool    `json:"complete"`
	Probability float64 `json:"probability"`
	Model       string  `json:"model"`
}

// Complete says whether the turn so far is finished (listen.Judge).
func (c *Client) Complete(ctx context.Context, pcm []byte) (bool, error) {
	v, err := c.Judge(ctx, pcm)
	return v.Complete, err
}

// Judge posts the turn so far and checks the reply against what the client
// was built for. A verdict from another checkpoint is an error, not a guess.
func (c *Client) Judge(ctx context.Context, pcm []byte) (Verdict, error) {
	// Neither is worth a round trip: the sidecar answers 400 to both.
	if len(pcm) == 0 {
		return Verdict{}, errors.New("smartturn: no audio to judge")
	}
	if len(pcm)%(bridge.BitsPerSample/8) != 0 {
		return Verdict{}, fmt.Errorf("smartturn: %d bytes is not whole %d-bit samples", len(pcm), bridge.BitsPerSample)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(pcm))
	if err != nil {
		return Verdict{}, fmt.Errorf("smartturn: build request: %w", err)
	}
	req.Header.Set("Content-Type", ContentType)
	req.Header.Set("Accept", "application/json")

	resp, err := c.cfg.HTTP.Do(req)
	if err != nil {
		return Verdict{}, fmt.Errorf("smartturn judge: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return Verdict{}, fmt.Errorf("smartturn judge: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	var v Verdict
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return Verdict{}, fmt.Errorf("smartturn judge: decode reply: %w", err)
	}
	switch {
	case v.Model != c.cfg.Model:
		return Verdict{}, fmt.Errorf("smartturn judge: sidecar serves %q, client is built for %q", v.Model, c.cfg.Model)
	case math.IsNaN(v.Probability) || v.Probability < 0 || v.Probability > 1:
		return Verdict{}, fmt.Errorf("smartturn judge: probability %v is not one", v.Probability)
	}
	return v, nil
}
