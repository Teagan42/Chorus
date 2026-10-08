// Package kokoro is the phase-1 speech end of the cascade: Kokoro-82M behind
// Kokoro-FastAPI's /v1/audio/speech, resampled to the device's rate
// (SPEC §10, §14 item 3).
//
// Raw `pcm` rather than `wav`: the WAV this endpoint returns declares a data
// chunk of 0xFFFFFFFF even on a non-streaming response, so a parser that
// trusts the header reads past the buffer. The bytes after the header are the
// same samples, so the header is pure liability.
//
// Measured on the CPU image, 2026-10-07: `stream: true` flushes nothing early
// -- the whole response lands at once, 0.56-0.92 s for a clause and 3.9 s for a
// paragraph -- so this asks for a complete response and the satellite's clause
// chunking is what keeps audio flowing (internal/satellite/chunk.go).
package kokoro

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/satellite"
)

// DefaultVoice is the endpoint's own default, named here so the version
// recorded against a turn does not change when the sidecar is upgraded.
const DefaultVoice = "af_heart"

// DefaultModel is the only model this endpoint serves; anything else is a 400.
const DefaultModel = "kokoro"

// DefaultTimeout bounds one request. Unlike the turn engine, which streams for
// as long as the model talks, a clause is a bounded render -- and a hung
// sidecar would otherwise park the feeder goroutine for the whole session.
// Generous because the CPU image runs at roughly a third of real time.
const DefaultTimeout = 30 * time.Second

// errBody bounds how much of a failed response is quoted. The voice-not-found
// error lists all 72 voices, which belongs in neither a log line nor an error.
const errBody = 1 << 10

// Config wires a Synth. Everything that does I/O is injected, so `task test`
// stays hermetic (CONTRIBUTING §2).
type Config struct {
	// BaseURL is the endpoint root, e.g. http://127.0.0.1:8880.
	BaseURL string

	// Voice defaults to DefaultVoice. An unknown one fails every utterance, so
	// it is worth checking against /v1/audio/voices at startup.
	Voice string

	// Model defaults to DefaultModel.
	Model string

	// Speed defaults to 1. The endpoint takes it as a multiplier.
	Speed float64

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Synth renders one clause of text as device-ready PCM.
type Synth struct {
	cfg  Config
	url  string
	down resampler
}

// Synth is the seam the satellite speaks through (SPEC §4.2).
var _ satellite.Synth = (*Synth)(nil)

// New validates the wiring and applies defaults.
func New(cfg Config) (*Synth, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("kokoro: base url is required")
	}
	if cfg.Voice == "" {
		cfg.Voice = DefaultVoice
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Speed == 0 {
		cfg.Speed = 1
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Synth{
		cfg:  cfg,
		url:  strings.TrimSuffix(cfg.BaseURL, "/") + "/v1/audio/speech",
		down: newResampler(),
	}, nil
}

// request is one POST /v1/audio/speech body.
type request struct {
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`

	// Stream is sent explicitly because the endpoint defaults it to true, and a
	// chunked response buys nothing when the whole buffer is the return value.
	Stream bool `json:"stream"`
}

// Synthesize renders text at the device's 16 kHz, signed 16-bit, mono.
func (s *Synth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	// Blank text is silence, not a request. The endpoint answers 400 "contains
	// no speakable text", and the satellite treats a failed render as the end of
	// the utterance -- so one stray whitespace delta would cut off everything
	// after it (internal/satellite/speaker.go).
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}

	body, err := json.Marshal(request{
		Model:          s.cfg.Model,
		Input:          text,
		Voice:          s.cfg.Voice,
		ResponseFormat: "pcm",
		Speed:          s.cfg.Speed,
		Stream:         false,
	})
	if err != nil {
		return nil, fmt.Errorf("kokoro: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("kokoro: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.cfg.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kokoro speech: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Quoted because the status alone does not separate an unknown voice
		// from an unknown model from unspeakable text: all three are 400 and
		// only the body says which.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return nil, fmt.Errorf("kokoro speech: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	pcm, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("kokoro speech: read audio: %w", err)
	}
	samples, err := decode(pcm)
	if err != nil {
		return nil, err
	}
	return encode(s.down.run(samples)), nil
}

// decode reads little-endian samples. An odd length is a truncated response,
// not a sample to round off: every later sample would be a byte out of phase
// and the utterance would come out as noise.
func decode(pcm []byte) ([]int16, error) {
	if len(pcm)%2 != 0 {
		return nil, fmt.Errorf("kokoro speech: %d bytes is not whole 16-bit samples", len(pcm))
	}
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return out, nil
}

func encode(samples []int16) []byte {
	out := make([]byte, 2*len(samples))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(s))
	}
	return out
}
