package openai_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/openai"
	"github.com/teagan42/chorus/internal/provider/pcm"
)

// wav24k is what a server answers: a WAV at its own rate, here 24 kHz mono
// with the streaming writer's unknown-length data size.
func wav24k(samples int) []byte {
	in := make([]int16, samples)
	for i := range in {
		in[i] = int16(i % 1000)
	}
	b := pcm.WAV(pcm.Encode(in))
	// Re-declare the rate as 24 kHz and the length as unknown.
	b[24], b[25], b[26], b[27] = 0xC0, 0x5D, 0, 0
	b[40], b[41], b[42], b[43] = 0xFF, 0xFF, 0xFF, 0xFF
	return b
}

func TestSynthAsksForWAVAndResamplesToTheDevice(t *testing.T) {
	var seen map[string]any
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &seen)
		_, _ = w.Write(wav24k(2400))
	}))
	t.Cleanup(srv.Close)

	s, err := openai.NewSynth(openai.SynthConfig{BaseURL: srv.URL + "/v1/", APIKey: "sk-test", Model: "tts-1", Voice: "nova"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := s.Synthesize(context.Background(), "Hello there.")
	if err != nil {
		t.Fatal(err)
	}
	// A tenth of a second at 24 kHz is a tenth at 16 kHz: 1600 samples.
	if len(out) != 2*1600 {
		t.Errorf("got %d bytes, want %d", len(out), 2*1600)
	}
	if path != "/v1/audio/speech" {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer sk-test" {
		t.Errorf("authorization = %q", auth)
	}
	if seen["response_format"] != "wav" || seen["model"] != "tts-1" || seen["voice"] != "nova" || seen["input"] != "Hello there." {
		t.Errorf("request = %v", seen)
	}
	if _, ok := seen["speed"]; ok {
		t.Error("speed sent though unset; not every server takes it")
	}
	if s.Version() != "tts-1/nova" {
		t.Errorf("version = %q", s.Version())
	}
}

func TestSynthSendsNothingForBlankText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("blank text reached the server")
	}))
	t.Cleanup(srv.Close)
	s, _ := openai.NewSynth(openai.SynthConfig{BaseURL: srv.URL, Model: "m", Voice: "v"})
	if out, err := s.Synthesize(context.Background(), " \n"); out != nil || err != nil {
		t.Errorf("got %v, %v", out, err)
	}
}

// The key never appears in an error, though the body is quoted.
func TestFailuresQuoteTheBodyNotTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"voice 'nova' not found"}`, http.StatusBadRequest)
	}))
	t.Cleanup(srv.Close)
	s, _ := openai.NewSynth(openai.SynthConfig{BaseURL: srv.URL, APIKey: "sk-secret", Model: "m", Voice: "nova"})
	_, err := s.Synthesize(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err = %v", err)
	}
	tr, _ := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: srv.URL, APIKey: "sk-secret", Model: "whisper-1"})
	_, err = tr.Transcribe(context.Background(), []byte{0, 0})
	if err == nil || !strings.Contains(err.Error(), "400") || strings.Contains(err.Error(), "sk-secret") {
		t.Errorf("err = %v", err)
	}
}

func TestTranscriberUploadsADeviceWAV(t *testing.T) {
	var file []byte
	var fields map[string]string
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path != "/v1/audio/transcriptions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		fields = map[string]string{}
		for k, v := range r.MultipartForm.Value {
			fields[k] = v[0]
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		file, _ = io.ReadAll(f)
		_, _ = w.Write([]byte(`{"text":"turn on the kitchen lights"}`))
	}))
	t.Cleanup(srv.Close)

	tr, err := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: srv.URL, Model: "whisper-large-v3-turbo", Language: "en"})
	if err != nil {
		t.Fatal(err)
	}
	audio := []byte{1, 0, 2, 0}
	got, err := tr.Transcribe(context.Background(), audio)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "turn on the kitchen lights" {
		t.Errorf("text = %q", got.Text)
	}
	if string(file) != string(pcm.WAV(audio)) {
		t.Error("the file is not the utterance in the device's WAV")
	}
	if fields["model"] != "whisper-large-v3-turbo" || fields["response_format"] != "json" || fields["language"] != "en" {
		t.Errorf("fields = %v", fields)
	}
	if auth != "" {
		t.Errorf("authorization %q sent with no key", auth)
	}
	if tr.Version() != "whisper-large-v3-turbo" {
		t.Errorf("version = %q", tr.Version())
	}
}

func TestTranscriberSendsNothingForSilenceAndRefusesTornSamples(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("reached the server")
	}))
	t.Cleanup(srv.Close)
	tr, _ := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: srv.URL, Model: "m"})
	if got, err := tr.Transcribe(context.Background(), nil); got.Text != "" || err != nil {
		t.Errorf("silence: %v, %v", got, err)
	}
	if _, err := tr.Transcribe(context.Background(), []byte{1, 2, 3}); err == nil {
		t.Error("a torn sample was sent")
	}
}

// Model and voice are what the journal records; a missing one is a startup
// error rather than a server default nobody wrote down.
func TestNewRefusesAnUnnamedModelOrVoice(t *testing.T) {
	for _, cfg := range []openai.SynthConfig{
		{Model: "m", Voice: "v"},
		{BaseURL: "http://x", Voice: "v"},
		{BaseURL: "http://x", Model: "m"},
	} {
		if _, err := openai.NewSynth(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
	for _, cfg := range []openai.TranscriberConfig{{Model: "m"}, {BaseURL: "http://x"}} {
		if _, err := openai.NewTranscriber(cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
}

func TestHealthURLIsTheModelList(t *testing.T) {
	for _, base := range []string{"https://api.groq.com/openai", "https://api.groq.com/openai/", "https://api.groq.com/openai/v1"} {
		if got := openai.HealthURL(base); got != "https://api.groq.com/openai/v1/models" {
			t.Errorf("%s: %s", base, got)
		}
	}
}
