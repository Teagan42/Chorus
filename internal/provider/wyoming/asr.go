package wyoming

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/stt"
)

// chunkBytes is how much audio each audio-chunk carries: 64 ms, the
// 1024-sample chunk Home Assistant's own pipeline sends.
const chunkBytes = 1024 * bridge.BitsPerSample / 8

// TranscriberConfig wires a Transcriber.
type TranscriberConfig struct {
	// Address is the server's host:port, e.g. 10.0.0.5:10300.
	Address string

	// Model names what the server runs, for the journal's STT slot. It is
	// not sent: the protocol has the server choose, so this is the
	// household's word for what it started the server with (SPEC §8).
	Model string

	// Language is sent with transcribe when set.
	Language string

	// Timeout defaults to DefaultTimeout.
	Timeout time.Duration
}

// Transcriber decodes one utterance of device PCM.
type Transcriber struct {
	cfg TranscriberConfig
}

// Transcriber is the seam the utterance stream decodes through (SPEC §4.5).
var _ stt.Transcriber = (*Transcriber)(nil)

// NewTranscriber validates the wiring and applies defaults. It does not
// dial.
func NewTranscriber(cfg TranscriberConfig) (*Transcriber, error) {
	addr, err := Address(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("wyoming asr: %w", err)
	}
	if cfg.Model == "" {
		return nil, errors.New("wyoming asr: model is required")
	}
	cfg.Address = addr
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Transcriber{cfg: cfg}, nil
}

// Version names what hears a turn, for the journal's STT slot.
func (t *Transcriber) Version() string { return t.cfg.Model }

// Transcribe streams the utterance as audio chunks and waits for the
// transcript.
func (t *Transcriber) Transcribe(ctx context.Context, audio []byte) (stt.Result, error) {
	// An utterance that closed before any audio arrived is ordinary
	// (internal/stt/utterance.go), not a request.
	if len(audio) == 0 {
		return stt.Result{}, nil
	}
	if len(audio)%(bridge.BitsPerSample/8) != 0 {
		return stt.Result{}, fmt.Errorf("wyoming asr: %d bytes is not whole samples", len(audio))
	}
	c, err := dial(ctx, t.cfg.Address, t.cfg.Timeout)
	if err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, err)
	}
	defer c.end()

	format := map[string]any{"rate": bridge.SampleRate, "width": bridge.BitsPerSample / 8, "channels": 1}
	transcribe := map[string]any{}
	if t.cfg.Language != "" {
		transcribe["language"] = t.cfg.Language
	}
	evs := []event{{Type: "transcribe", Data: transcribe}, {Type: "audio-start", Data: format}}
	for off := 0; off < len(audio); off += chunkBytes {
		evs = append(evs, event{Type: "audio-chunk", Data: format, Payload: audio[off:min(off+chunkBytes, len(audio))]})
	}
	evs = append(evs, event{Type: "audio-stop"})
	if err := c.send(evs...); err != nil {
		return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, cause(ctx, err))
	}
	for {
		ev, err := c.next()
		if err != nil {
			return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, cause(ctx, err))
		}
		switch ev.Type {
		case "transcript":
			return stt.Result{Text: ev.str("text")}, nil
		case "error":
			return stt.Result{}, fmt.Errorf("transcribe %s: %w", t.cfg.Model, ev.failed())
		}
		// transcript-start and transcript-chunk come first from a server
		// that streams; the final transcript is the whole of it.
	}
}

// Capabilities is what this provider declares as the ear, from
// schema/provider.cue (ADR-0102).
func (t *Transcriber) Capabilities() capability.Declaration {
	return capability.Of(capability.RoleSTT, "wyoming")
}
