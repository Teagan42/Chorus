package kokoro_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/provider/kokoro"
)

// speech is what the endpoint returns: raw little-endian samples at 24 kHz,
// here a quarter second of them.
func speech(samples int) []byte {
	out := make([]byte, 2*samples)
	for i := range samples {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(i%1000)))
	}
	return out
}

// serve runs a Synth against a handler, reporting the request body it saw.
func serve(t *testing.T, h http.HandlerFunc) (*kokoro.Synth, *request) {
	t.Helper()
	seen := &request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen.path = r.URL.Path
		if err := json.Unmarshal(body, seen); err != nil {
			t.Errorf("request body is not json: %v", err)
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)

	s, err := kokoro.New(kokoro.Config{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("new synth: %v", err)
	}
	return s, seen
}

type request struct {
	path           string
	Model          string  `json:"model"`
	Input          string  `json:"input"`
	Voice          string  `json:"voice"`
	ResponseFormat string  `json:"response_format"`
	Speed          float64 `json:"speed"`
	Stream         bool    `json:"stream"`
}

func audio(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		_, _ = w.Write(body)
	}
}

// Raw PCM, unstreamed, at the endpoint's defaults. Asking for wav instead
// would return a data chunk length of 0xFFFFFFFF that a parser cannot use, and
// leaving stream unset defaults it to true for no benefit.
func TestItAsksForRawUnstreamedPCM(t *testing.T) {
	s, seen := serve(t, audio(speech(6000)))
	if _, err := s.Synthesize(context.Background(), "Kitchen lights are on."); err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	switch {
	case seen.path != "/v1/audio/speech":
		t.Errorf("posted to %q", seen.path)
	case seen.ResponseFormat != "pcm":
		t.Errorf("response_format is %q, want pcm", seen.ResponseFormat)
	case seen.Stream:
		t.Error("stream is true, so the response arrives chunked for nothing")
	case seen.Input != "Kitchen lights are on.":
		t.Errorf("input is %q", seen.Input)
	case seen.Voice != kokoro.DefaultVoice:
		t.Errorf("voice is %q, want the pinned default", seen.Voice)
	case seen.Model != kokoro.DefaultModel:
		t.Errorf("model is %q", seen.Model)
	case seen.Speed != 1:
		t.Errorf("speed is %v, want 1", seen.Speed)
	}
}

// The device's rate is fixed at 16 kHz and the endpoint has no rate parameter,
// so audio that reaches the link unresampled plays a third too slow.
//
// verifies SPEC §3.2
func TestItReturnsAudioAtTheDeviceRate(t *testing.T) {
	s, _ := serve(t, audio(speech(24000))) // one second at 24 kHz
	pcm, err := s.Synthesize(context.Background(), "one second")
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if got, want := len(pcm)/2, 16000; got != want {
		t.Errorf("one second came back as %d samples, want %d", got, want)
	}
}

// A truncated response is noise if it is played: every sample after the odd
// byte is a byte out of phase.
func TestATruncatedSampleFailsRatherThanPlays(t *testing.T) {
	s, _ := serve(t, audio(speech(600)[:1199]))
	if _, err := s.Synthesize(context.Background(), "cut short"); err == nil {
		t.Fatal("an odd byte count must fail")
	}
}

// Three different mistakes -- unknown voice, unknown model, unspeakable text --
// are all 400s that only the body tells apart.
func TestItQuotesWhatTheEndpointComplainedAbout(t *testing.T) {
	s, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail":{"error":"validation_error","message":"Voice 'nope' not found."}}`))
	})
	_, err := s.Synthesize(context.Background(), "hello")
	if err == nil {
		t.Fatal("a 400 must fail the render")
	}
	if !strings.Contains(err.Error(), "Voice 'nope' not found") {
		t.Errorf("error does not name the cause: %v", err)
	}
}

// Blank text is silence and must not reach the endpoint: it answers 400, and
// the satellite reads a failed render as the end of the utterance, so one
// whitespace delta would cut off everything after it.
//
// verifies SPEC §4.2
func TestBlankTextMakesNoRequest(t *testing.T) {
	s, seen := serve(t, audio(speech(600)))
	for _, text := range []string{"", " ", "\n\t"} {
		pcm, err := s.Synthesize(context.Background(), text)
		if err != nil {
			t.Errorf("synthesize %q: %v", text, err)
		}
		if len(pcm) != 0 {
			t.Errorf("synthesize %q returned %d bytes", text, len(pcm))
		}
	}
	if seen.path != "" {
		t.Errorf("blank text reached %q", seen.path)
	}
}

// A barge-in cancels the stream's context while a render is in flight
// (internal/satellite/speaker.go).
//
// verifies SPEC §4.3
func TestACancelledContextAbandonsTheRender(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := serve(t, func(w http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
	})
	if _, err := s.Synthesize(ctx, "interrupt me"); !errors.Is(err, context.Canceled) {
		t.Errorf("error is %v, want context.Canceled", err)
	}
}

func TestNewRequiresAnEndpoint(t *testing.T) {
	if _, err := kokoro.New(kokoro.Config{}); err == nil {
		t.Fatal("a synth with no base url must not build")
	}
}

// The journal stamps this on every event, so a speech_truncated can say
// which voice was cut (SPEC §8). Model and voice together, because a voice
// id means nothing outside its model (ADR-0032); the defaults are spelled
// out, not read back from the sidecar, so an upgrade cannot move them.
//
// verifies SPEC §8
func TestVersionNamesTheModelAndVoice(t *testing.T) {
	cases := map[string]struct {
		cfg  kokoro.Config
		want string
	}{
		"defaults":           {kokoro.Config{}, kokoro.DefaultModel + "/" + kokoro.DefaultVoice},
		"a configured voice": {kokoro.Config{Voice: "bf_emma"}, "kokoro/bf_emma"},
		"another model":      {kokoro.Config{Model: "kokoro-v2", Voice: "am_adam"}, "kokoro-v2/am_adam"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.cfg.BaseURL = "http://kokoro.invalid"
			s, err := kokoro.New(tc.cfg)
			if err != nil {
				t.Fatalf("new synth: %v", err)
			}
			if got := s.Version(); got != tc.want {
				t.Errorf("version = %q, want %q", got, tc.want)
			}
		})
	}
}
