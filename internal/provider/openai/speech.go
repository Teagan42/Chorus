package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/teagan42/chorus/internal/provider/pcm"
	"github.com/teagan42/chorus/internal/satellite"
)

// SynthConfig wires a Synth. Everything that does I/O is injected, so `task
// test` stays hermetic (CONTRIBUTING §1).
type SynthConfig struct {
	// BaseURL is the server root, e.g. https://api.openai.com or
	// http://10.0.0.20:8000/v1.
	BaseURL string

	// APIKey is sent as a bearer token when set.
	APIKey string

	// Model and Voice are both required: they are recorded against every
	// turn, so there is no server default to drift from (SPEC §8).
	Model string
	Voice string

	// Speed is sent only when set; not every server takes it.
	Speed float64

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Synth renders one clause of text as device-ready PCM.
type Synth struct {
	cfg SynthConfig
	url string
}

// Synth is the seam the satellite speaks through (SPEC §4.2).
var _ satellite.Synth = (*Synth)(nil)

// NewSynth validates the wiring and applies defaults.
func NewSynth(cfg SynthConfig) (*Synth, error) {
	url, err := endpoint(cfg.BaseURL, "/audio/speech")
	if err != nil {
		return nil, fmt.Errorf("openai speech: %w", err)
	}
	if cfg.Model == "" || cfg.Voice == "" {
		return nil, errors.New("openai speech: model and voice are required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Synth{cfg: cfg, url: url}, nil
}

// Version names what voices a turn, as model/voice, for the journal's TTS
// slot: a voice id means nothing outside its model (ADR-0032).
func (s *Synth) Version() string { return s.cfg.Model + "/" + s.cfg.Voice }

type speechRequest struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed,omitempty"`
}

// Synthesize renders text at the device's 16 kHz, signed 16-bit, mono.
//
// WAV rather than `pcm`: OpenAI's raw PCM is 24 kHz by fiat, and other
// servers' is whatever their model renders at, unannounced. A WAV header
// says the rate, so whatever the server is, the resampler is told it.
func (s *Synth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	// Blank text is silence, not a request: a failed render ends the
	// utterance, so one stray whitespace delta would cut off the rest
	// (internal/satellite/speaker.go).
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	body, err := json.Marshal(speechRequest{
		Model: s.cfg.Model, Input: text, Voice: s.cfg.Voice,
		ResponseFormat: "wav", Speed: s.cfg.Speed,
	})
	if err != nil {
		return nil, fmt.Errorf("openai speech: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai speech: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	authorize(req, s.cfg.APIKey)

	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai speech %s: %w", s.Version(), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, failure("openai speech "+s.Version(), resp)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai speech %s: read audio: %w", s.Version(), err)
	}
	audio, err := pcm.ParseWAV(raw)
	if err != nil {
		return nil, fmt.Errorf("openai speech %s: %w", s.Version(), err)
	}
	out, err := pcm.ToDevice(audio.Samples, audio.Rate, audio.Channels)
	if err != nil {
		return nil, fmt.Errorf("openai speech %s: %w", s.Version(), err)
	}
	return out, nil
}
