//go:build models

// Model tier: needs a reachable Kokoro-FastAPI. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/provider/kokoro/ -kokoro-url http://127.0.0.1:8880
//
// The endpoint is a flag with no default so no household address lives in the
// repo. Without it these skip.
//
// What is tested here is the half the hermetic tier cannot reach: that the
// sidecar still renders at the rate the resampler is built around, and that a
// clause costs what SPEC §11 was budgeted against. Resampling itself is
// covered hermetically in resample_test.go.
package kokoro_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/provider/kokoro"
)

var (
	endpoint = flag.String("kokoro-url", "", "Kokoro-FastAPI base URL; skips when empty")
	voice    = flag.String("kokoro-voice", kokoro.DefaultVoice, "voice to exercise")
)

// renderBudget is generous on purpose: the CPU image runs at roughly a third of
// real time, and a paragraph is several seconds of audio.
const renderBudget = time.Minute

func synth(t *testing.T) *kokoro.Synth {
	t.Helper()
	if *endpoint == "" {
		t.Skip("no -kokoro-url")
	}
	s, err := kokoro.New(kokoro.Config{BaseURL: *endpoint, Voice: *voice})
	if err != nil {
		t.Fatalf("new synth: %v", err)
	}
	return s
}

// render returns one clause's audio and logs what it cost.
func render(t *testing.T, s *kokoro.Synth, text string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), renderBudget)
	defer cancel()

	start := time.Now()
	pcm, err := s.Synthesize(ctx, text)
	if err != nil {
		t.Fatalf("synthesize %q: %v", text, err)
	}
	// Logged every run: SPEC §11 budgets ~700 ms to first audio and a
	// regression here is invisible in a pass/fail.
	t.Logf("%q -> %v of audio in %v", text, duration(pcm), time.Since(start).Round(time.Millisecond))
	return pcm
}

func duration(pcm []byte) time.Duration {
	frames := len(pcm) / (bridge.BitsPerSample / 8)
	return time.Duration(frames) * time.Second / bridge.SampleRate
}

// A clause has to come back as plausible speech at the device's rate. Length is
// the only honest assertion against a real voice: the audio is not
// reproducible, but a clause that renders as a tenth of a second or as a minute
// is a broken request either way.
func TestARealEndpointRendersAClause(t *testing.T) {
	pcm := render(t, synth(t), "Kitchen lights are on.")

	if len(pcm)%(bridge.BitsPerSample/8) != 0 {
		t.Errorf("%d bytes is not whole frames", len(pcm))
	}
	if d := duration(pcm); d < 500*time.Millisecond || d > 5*time.Second {
		t.Errorf("a five-word clause rendered as %v of audio", d)
	}
}

// The assumption the resampler is built on, and the one failure nothing else
// would catch: a sidecar upgrade that changed the render rate would play every
// utterance at the wrong pitch, with no error anywhere.
//
// verifies SPEC §3.2
func TestTheEndpointStillRendersAtTwentyFourKilohertz(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -kokoro-url")
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderBudget)
	defer cancel()

	// Asked for as wav purely to read the rate off the header. The package
	// itself takes raw pcm, which carries no rate at all -- which is why this
	// has to be checked rather than trusted.
	body, _ := json.Marshal(map[string]any{
		"input": "Kitchen lights are on.", "voice": *voice,
		"response_format": "wav", "stream": false,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, *endpoint+"/v1/audio/speech", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("speech: %v", err)
	}
	defer resp.Body.Close()

	// The fmt chunk, not the data chunk: this endpoint writes a data length of
	// 0xFFFFFFFF even on a complete response.
	head := make([]byte, 36)
	if _, err := io.ReadFull(resp.Body, head); err != nil {
		t.Fatalf("read wav header: %v", err)
	}
	if !bytes.Equal(head[0:4], []byte("RIFF")) || !bytes.Equal(head[12:16], []byte("fmt ")) {
		t.Fatalf("not a wav header: %q", head[:16])
	}
	channels := binary.LittleEndian.Uint16(head[22:24])
	rate := binary.LittleEndian.Uint32(head[24:28])
	bits := binary.LittleEndian.Uint16(head[34:36])
	t.Logf("sidecar renders %d Hz, %d channel, %d-bit", rate, channels, bits)

	if rate != 24000 || channels != 1 || bits != bridge.BitsPerSample {
		t.Errorf("the resampler assumes 24 kHz mono 16-bit, got %d Hz %d channel %d-bit", rate, channels, bits)
	}
}

// A misconfigured voice must fail the render rather than return silence: the
// satellite reads a failed render as the end of the utterance, and silence
// would look like a working assistant that nobody can hear.
func TestAnUnknownVoiceFailsTheRender(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -kokoro-url")
	}
	s, err := kokoro.New(kokoro.Config{BaseURL: *endpoint, Voice: "definitely_not_a_voice"})
	if err != nil {
		t.Fatalf("new synth: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), renderBudget)
	defer cancel()
	if _, err := s.Synthesize(ctx, "hello"); err == nil {
		t.Fatal("an unknown voice must fail the render")
	} else {
		t.Logf("unknown voice: %v", err)
	}
}

// The latency SPEC §11 is budgeted against, measured rather than asserted: a
// paragraph costs seconds on CPU, which is why the satellite cuts a delta into
// clauses and renders them one at a time (internal/satellite/chunk.go).
func TestWhatAParagraphCosts(t *testing.T) {
	s := synth(t)
	render(t, s, "OK.")
	render(t, s, "I found three lasagne recipes in your library. The first is a classic beef ragu with bechamel. The second is a vegetarian version with spinach and ricotta.")
}
