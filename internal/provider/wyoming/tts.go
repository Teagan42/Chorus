package wyoming

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/pcm"
	"github.com/teagan42/chorus/internal/satellite"
)

// SynthConfig wires a Synth.
type SynthConfig struct {
	// Address is the server's host:port, e.g. 10.0.0.5:10200.
	Address string

	// Voice is sent with every synthesize and recorded against every turn,
	// so it is required: the server's default is not written down anywhere
	// the journal can see (SPEC §8).
	Voice string

	// Speaker picks one speaker of a multi-speaker voice, when set.
	Speaker string

	// Timeout defaults to DefaultTimeout.
	Timeout time.Duration
}

// Synth renders one clause of text as device-ready PCM.
type Synth struct {
	cfg SynthConfig
}

// Synth is the seam the satellite speaks through (SPEC §4.2).
var _ satellite.Synth = (*Synth)(nil)

// NewSynth validates the wiring and applies defaults. It does not dial.
func NewSynth(cfg SynthConfig) (*Synth, error) {
	addr, err := Address(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("wyoming tts: %w", err)
	}
	if cfg.Voice == "" {
		return nil, errors.New("wyoming tts: voice is required")
	}
	cfg.Address = addr
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	return &Synth{cfg: cfg}, nil
}

// Version names what voices a turn: the voice, with its speaker when one is
// chosen. A Piper voice id already names its model (ADR-0032).
func (s *Synth) Version() string {
	if s.cfg.Speaker != "" {
		return s.cfg.Voice + "/" + s.cfg.Speaker
	}
	return s.cfg.Voice
}

// Synthesize renders text at the device's 16 kHz, signed 16-bit, mono.
func (s *Synth) Synthesize(ctx context.Context, text string) ([]byte, error) {
	// Blank text is silence, not a request: a failed render ends the
	// utterance (internal/satellite/speaker.go).
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	c, err := dial(ctx, s.cfg.Address, s.cfg.Timeout)
	if err != nil {
		return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), err)
	}
	defer c.end()

	voice := map[string]any{"name": s.cfg.Voice}
	if s.cfg.Speaker != "" {
		voice["speaker"] = s.cfg.Speaker
	}
	if err := c.send(event{Type: "synthesize", Data: map[string]any{"text": text, "voice": voice}}); err != nil {
		return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), cause(ctx, err))
	}

	var (
		raw            []byte
		rate, channels int
	)
	for {
		ev, err := c.next()
		if err != nil {
			return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), cause(ctx, err))
		}
		switch ev.Type {
		case "audio-start", "audio-chunk":
			r, w, ch := ev.num("rate"), ev.num("width"), ev.num("channels")
			if w != 0 && w != 2 {
				return nil, fmt.Errorf("wyoming tts %s: %d-byte samples, want 2", s.Version(), w)
			}
			if rate == 0 {
				rate, channels = r, ch
			} else if r != 0 && (r != rate || ch != channels) {
				return nil, fmt.Errorf("wyoming tts %s: format changed mid-clause from %d Hz x%d to %d Hz x%d", s.Version(), rate, channels, r, ch)
			}
			raw = append(raw, ev.Payload...)
			if len(raw) > maxPayload {
				return nil, fmt.Errorf("wyoming tts %s: clause over %d bytes", s.Version(), maxPayload)
			}
		case "audio-stop":
			if len(raw) == 0 {
				return nil, nil
			}
			if rate == 0 {
				return nil, fmt.Errorf("wyoming tts %s: audio with no rate", s.Version())
			}
			samples, err := pcm.Decode(raw)
			if err != nil {
				return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), err)
			}
			out, err := pcm.ToDevice(samples, rate, max(channels, 1))
			if err != nil {
				return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), err)
			}
			return out, nil
		case "error":
			return nil, fmt.Errorf("wyoming tts %s: %w", s.Version(), ev.failed())
		}
	}
}

// Capabilities is what this provider declares as the voice, from
// schema/provider.cue (ADR-0102).
func (s *Synth) Capabilities() capability.Declaration {
	return capability.Of(capability.RoleTTS, "wyoming")
}
