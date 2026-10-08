// Package speaches is the phase-1 hearing end of the cascade: Parakeet TDT
// 0.6B v2 behind speaches' OpenAI-compatible /v1/audio/transcriptions
// (SPEC §10, §14 item 3).
//
// speaches rather than NVIDIA's own NIM because the NIM wants an NGC key, a
// GPU and an unpinned `latest` tag, while speaches publishes a semver-tagged
// CPU image serving the same multipart contract.
//
// Pinned at 0.9.0-rc.1, the first Parakeet tag. rc.2 and rc.3 exist; rc.1
// stays: its source is what this behaviour was read from, and the only tag
// any measurement touched (ADR-0024).
//
// The Parakeet path answers `json` and `text` only and refuses `verbose_json`
// before decoding, so stt.Result carries text and nothing else. `language` is
// accepted and ignored by it; Whisper models on the same endpoint honour it.
package speaches

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/stt"
)

// DefaultModel is the ONNX export speaches' Parakeet executor is written
// against: its registry filter matches `istupakov/parakeet-tdt` and nothing
// else, so NVIDIA's own repo id is not loadable here. Named in full so the
// model recorded against a turn does not move when the sidecar is upgraded.
const DefaultModel = "istupakov/parakeet-tdt-0.6b-v2-onnx"

// DefaultTimeout bounds one request. An utterance is at most 30 s of audio
// (stt.DefaultMaxUtterance), and a hung sidecar would otherwise park the
// utterance's decode goroutine for the whole session. Measured 2026-10-08
// (int8 ONNX export behind a stand-in for the pinned server, 4-vCPU Xeon
// 2.8 GHz; models tier): 1 s of speech in 0.2-0.6 s, 3 s in ~0.5 s, 10 s in
// 1.2-2.8 s, and a 7.4 s final in 2.4 s while the partial Finish abandoned
// was still decoding server-side -- a cancel frees the client, not the
// sidecar's CPU. A 30 s final would cost under 10 s there; this is 3x that.
const DefaultTimeout = 30 * time.Second

// errBody bounds how much of a failed response is quoted. The unsupported-model
// 404 is one sentence; a FastAPI validation error is a JSON list of them.
const errBody = 1 << 10

// Config wires a Transcriber. Everything that does I/O is injected, so `task
// test` stays hermetic (CONTRIBUTING §1).
type Config struct {
	// BaseURL is the endpoint root, e.g. http://127.0.0.1:8000.
	BaseURL string

	// Model defaults to DefaultModel. It must already be downloaded: the
	// endpoint answers 404 for one it has not, which `task stt:up` does.
	Model string

	// Language is sent only when set. Parakeet v2 is English-only and ignores
	// it; a Whisper model behind the same endpoint uses it.
	Language string

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Transcriber decodes one utterance of device PCM.
type Transcriber struct {
	cfg Config
	url string
}

// Transcriber is the seam the utterance stream decodes through (SPEC §4.5).
var _ stt.Transcriber = (*Transcriber)(nil)

// New validates the wiring and applies defaults.
func New(cfg Config) (*Transcriber, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("speaches: base url is required")
	}
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Transcriber{
		cfg: cfg,
		url: strings.TrimSuffix(cfg.BaseURL, "/") + "/v1/audio/transcriptions",
	}, nil
}

// Version names what hears a turn, for the journal's STT slot (SPEC §8). The
// model id is already a stable name, so unlike a prompt it needs no hash.
func (t *Transcriber) Version() string { return t.cfg.Model }

// response is the `json` response format: text, and nothing the Parakeet path
// fills. The Whisper path adds usage, which nothing here reads.
type response struct {
	Text string `json:"text"`
}

// Transcribe uploads the utterance as a WAV and returns what was heard.
func (t *Transcriber) Transcribe(ctx context.Context, pcm []byte) (stt.Result, error) {
	// Silence is not a request. The endpoint answers 400 to an empty file, and
	// an utterance that closed before any audio arrived is ordinary
	// (internal/stt/utterance.go), not a failure to report.
	if len(pcm) == 0 {
		return stt.Result{}, nil
	}
	// An odd byte is a torn sample: every sample after it is a byte out of
	// phase, and the model would be asked to transcribe noise.
	if len(pcm)%(bridge.BitsPerSample/8) != 0 {
		return stt.Result{}, fmt.Errorf("speaches: %d bytes is not whole samples", len(pcm))
	}

	body, contentType, err := t.form(pcm)
	if err != nil {
		return stt.Result{}, fmt.Errorf("speaches: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return stt.Result{}, fmt.Errorf("speaches: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := t.cfg.HTTP.Do(req)
	if err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Quoted because the status alone does not separate a model that is
		// not downloaded from one the server cannot serve from audio it could
		// not decode; only the body says which.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return stt.Result{}, fmt.Errorf("transcribe %s: %s: %s", t.cfg.Model, resp.Status, strings.TrimSpace(string(msg)))
	}

	var out response
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: decode response: %w", t.cfg.Model, err)
	}
	return stt.Result{Text: out.Text}, nil
}

// form builds the multipart body. The file part carries its own media type:
// the endpoint decodes by content rather than by header, but a part labelled
// octet-stream is one less clue in a request log.
func (t *Transcriber) form(pcm []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="utterance.wav"`)
	hdr.Set("Content-Type", "audio/wav")
	part, err := w.CreatePart(hdr)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(wav(pcm)); err != nil {
		return nil, "", err
	}

	fields := [][2]string{
		{"model", t.cfg.Model},
		// Explicit even though it is the default: the response decoder is
		// written against this shape, and a server default that moved would
		// otherwise change what Transcribe returns with no change here.
		{"response_format", "json"},
	}
	if t.cfg.Language != "" {
		fields = append(fields, [2]string{"language", t.cfg.Language})
	}
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
