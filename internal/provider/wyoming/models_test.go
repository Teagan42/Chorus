//go:build models

// Model tier: needs reachable Wyoming servers, such as Home Assistant's
// piper and faster-whisper add-ons. Run with `task test:models`, pointing it
// at them:
//
//	go test -tags=models ./internal/provider/wyoming/ \
//	  -wyoming-tts homeassistant.local:10200 -wyoming-voice en_US-lessac-medium \
//	  -wyoming-stt homeassistant.local:10300
//
// Addresses are flags with no default so no household address lives in the
// repo. Without them these skip. The ear is checked by a round trip through
// the voice, since only real speech says anything about a real ear.
package wyoming

import (
	"context"
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
)

var (
	ttsAddr = flag.String("wyoming-tts", "", "Wyoming TTS host:port; skips when empty")
	voice   = flag.String("wyoming-voice", "", "voice the TTS server serves, e.g. en_US-lessac-medium")
	sttAddr = flag.String("wyoming-stt", "", "Wyoming ASR host:port; skips the ear when empty")
)

func realSynth(t *testing.T) *Synth {
	t.Helper()
	if *ttsAddr == "" || *voice == "" {
		t.Skip("no -wyoming-tts and -wyoming-voice")
	}
	s, err := NewSynth(SynthConfig{Address: *ttsAddr, Voice: *voice})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func audioLength(pcm []byte) time.Duration {
	return time.Duration(len(pcm)/(bridge.BitsPerSample/8)) * time.Second / bridge.SampleRate
}

func speak(t *testing.T, s *Synth, text string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	start := time.Now()
	pcm, err := s.Synthesize(ctx, text)
	if err != nil {
		t.Fatalf("synthesize %q: %v", text, err)
	}
	// Logged every run: SPEC §11 budgets ~700 ms to first audio.
	t.Logf("%q -> %v of audio in %v", text, audioLength(pcm), time.Since(start).Round(time.Millisecond))
	return pcm
}

// A clause comes back as plausible speech at the device's rate, whatever
// rate the server rendered at: length is the honest assertion against a
// real voice.
func TestARealServerRendersAClause(t *testing.T) {
	pcm := speak(t, realSynth(t), "Kitchen lights are on.")
	if len(pcm)%(bridge.BitsPerSample/8) != 0 {
		t.Errorf("%d bytes is not whole frames", len(pcm))
	}
	if d := audioLength(pcm); d < 500*time.Millisecond || d > 5*time.Second {
		t.Errorf("a five-word clause rendered as %v of audio", d)
	}
}

// What the voice says, the ear hears: the resampled audio is speech a real
// recogniser can read, not noise at the wrong rate.
func TestARealEarHearsTheRealVoice(t *testing.T) {
	s := realSynth(t)
	if *sttAddr == "" {
		t.Skip("no -wyoming-stt")
	}
	ear, err := NewTranscriber(TranscriberConfig{Address: *sttAddr, Model: "under-test"})
	if err != nil {
		t.Fatal(err)
	}
	pcm := speak(t, s, "Turn off the kitchen lights.")
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()
	start := time.Now()
	res, err := ear.Transcribe(ctx, pcm)
	if err != nil {
		t.Fatalf("transcribe: %v", err)
	}
	t.Logf("heard %q in %v", res.Text, time.Since(start).Round(time.Millisecond))
	if got := strings.ToLower(res.Text); !strings.Contains(got, "kitchen") || !strings.Contains(got, "light") {
		t.Errorf("heard %q, want the kitchen lights", res.Text)
	}
}
