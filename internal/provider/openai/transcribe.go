package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/pcm"
	"github.com/teagan42/chorus/internal/stt"
)

// TranscriberConfig wires a Transcriber.
type TranscriberConfig struct {
	// BaseURL is the server root, with or without its /v1.
	BaseURL string

	// APIKey is sent as a bearer token when set.
	APIKey string

	// Model is required: it is recorded against every turn (SPEC §8), and
	// whisper-1 on one server is not whisper-1 on another.
	Model string

	// Language is sent only when set, as an ISO-639-1 code.
	Language string

	// HTTP defaults to a client bounded by DefaultTimeout.
	HTTP *http.Client
}

// Transcriber decodes one utterance of device PCM.
type Transcriber struct {
	cfg TranscriberConfig
	url string
}

// Transcriber is the seam the utterance stream decodes through (SPEC §4.5).
var _ stt.Transcriber = (*Transcriber)(nil)

// NewTranscriber validates the wiring and applies defaults.
func NewTranscriber(cfg TranscriberConfig) (*Transcriber, error) {
	url, err := endpoint(cfg.BaseURL, "/audio/transcriptions")
	if err != nil {
		return nil, fmt.Errorf("openai transcription: %w", err)
	}
	if cfg.Model == "" {
		return nil, errors.New("openai transcription: model is required")
	}
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: DefaultTimeout}
	}
	return &Transcriber{cfg: cfg, url: url}, nil
}

// Version names what hears a turn, for the journal's STT slot (SPEC §8).
func (t *Transcriber) Version() string { return t.cfg.Model }

// Transcribe uploads the utterance as a device-format WAV and returns what
// was heard.
func (t *Transcriber) Transcribe(ctx context.Context, audio []byte) (stt.Result, error) {
	// An utterance that closed before any audio arrived is ordinary
	// (internal/stt/utterance.go), and servers answer 400 to an empty file.
	if len(audio) == 0 {
		return stt.Result{}, nil
	}
	if len(audio)%(bridge.BitsPerSample/8) != 0 {
		return stt.Result{}, fmt.Errorf("openai transcription: %d bytes is not whole samples", len(audio))
	}
	body, contentType, err := t.form(audio)
	if err != nil {
		return stt.Result{}, fmt.Errorf("openai transcription: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return stt.Result{}, fmt.Errorf("openai transcription: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	authorize(req, t.cfg.APIKey)

	resp, err := t.cfg.HTTP.Do(req)
	if err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stt.Result{}, failure("transcribe "+t.cfg.Model, resp)
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: decode response: %w", t.cfg.Model, err)
	}
	return stt.Result{Text: out.Text}, nil
}

func (t *Transcriber) form(audio []byte) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", `form-data; name="file"; filename="utterance.wav"`)
	hdr.Set("Content-Type", "audio/wav")
	part, err := w.CreatePart(hdr)
	if err != nil {
		return nil, "", err
	}
	if _, err := part.Write(pcm.WAV(audio)); err != nil {
		return nil, "", err
	}
	// json explicitly: the decoder above is written against its shape, and
	// a server whose default is text would otherwise fail every decode.
	fields := [][2]string{{"model", t.cfg.Model}, {"response_format", "json"}}
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

// Capabilities is what this provider declares as the ear, from
// schema/provider.cue (ADR-0102).
func (t *Transcriber) Capabilities() capability.Declaration {
	return capability.Of(capability.RoleSTT, "openai")
}
