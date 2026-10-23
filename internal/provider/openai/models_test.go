//go:build models

// Model tier: needs a reachable server with OpenAI's audio API. Run with
// `task test:models`, pointing it at one:
//
//	go test -tags=models ./internal/provider/openai/ \
//	  -openai-url http://127.0.0.1:8000 -openai-tts-model tts-1 -openai-voice alloy \
//	  -openai-stt-model whisper-1
//
// OPENAI_API_KEY is read from the environment when the server wants one, so
// no key is ever on a command line. Without a URL these skip. The ear is
// checked by a round trip through the voice; a server serving only one half
// can be given only that half's flags.
package openai_test

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/provider/openai"
)

var (
	baseURL  = flag.String("openai-url", "", "server root; skips when empty")
	ttsModel = flag.String("openai-tts-model", "", "speech model, e.g. tts-1")
	voice    = flag.String("openai-voice", "", "voice, e.g. alloy")
	sttModel = flag.String("openai-stt-model", "", "transcription model, e.g. whisper-1; skips the ear when empty")
)

func realSynth(t *testing.T) *openai.Synth {
	t.Helper()
	if *baseURL == "" || *ttsModel == "" || *voice == "" {
		t.Skip("no -openai-url, -openai-tts-model and -openai-voice")
	}
	s, err := openai.NewSynth(openai.SynthConfig{BaseURL: *baseURL, APIKey: os.Getenv("OPENAI_API_KEY"), Model: *ttsModel, Voice: *voice})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func audioLength(pcm []byte) time.Duration {
	return time.Duration(len(pcm)/(bridge.BitsPerSample/8)) * time.Second / bridge.SampleRate
}

func speak(t *testing.T, s *openai.Synth, text string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), openai.DefaultTimeout)
	defer cancel()
	start := time.Now()
	pcm, err := s.Synthesize(ctx, text)
	if err != nil {
		t.Fatalf("synthesize %q: %v", text, err)
	}
	t.Logf("%q -> %v of audio in %v", text, audioLength(pcm), time.Since(start).Round(time.Millisecond))
	return pcm
}

// A clause comes back as plausible speech at the device's rate, whatever
// rate the server's WAV declared.
func TestARealServerRendersAClause(t *testing.T) {
	pcm := speak(t, realSynth(t), "Kitchen lights are on.")
	if d := audioLength(pcm); d < 500*time.Millisecond || d > 5*time.Second {
		t.Errorf("a five-word clause rendered as %v of audio", d)
	}
}

// What the voice says, the ear hears.
func TestARealEarHearsTheRealVoice(t *testing.T) {
	s := realSynth(t)
	if *sttModel == "" {
		t.Skip("no -openai-stt-model")
	}
	ear, err := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: *baseURL, APIKey: os.Getenv("OPENAI_API_KEY"), Model: *sttModel})
	if err != nil {
		t.Fatal(err)
	}
	pcm := speak(t, s, "Turn off the kitchen lights.")
	ctx, cancel := context.WithTimeout(context.Background(), openai.DefaultTimeout)
	defer cancel()
	res, err := ear.Transcribe(ctx, pcm)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	t.Logf("heard %q", res.Text)
	if got := strings.ToLower(res.Text); !strings.Contains(got, "kitchen") || !strings.Contains(got, "light") {
		t.Errorf("heard %q, want the kitchen lights", res.Text)
	}
}
