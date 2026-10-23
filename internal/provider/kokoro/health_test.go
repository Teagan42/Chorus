package kokoro_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/kokoro"
)

// voicesAnswer serves a fixed /v1/audio/voices reply in-process.
type voicesAnswer struct {
	status int
	body   string
	asked  string
}

func (v *voicesAnswer) RoundTrip(r *http.Request) (*http.Response, error) {
	v.asked = r.Method + " " + r.URL.String()
	status := v.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Body: io.NopCloser(strings.NewReader(v.body)), Request: r,
	}, nil
}

// Part of Kokoro-FastAPI v0.9.0's list: the voices the household corpus
// renders in, and a few more.
const kokoroVoices = `{"voices":["af_alloy","af_bella","af_heart","af_nicole","af_sarah","af_sky","am_adam","am_michael","bf_emma","bm_george"]}`

func synthOn(t *testing.T, voice string, v *voicesAnswer) *kokoro.Synth {
	t.Helper()
	s, err := kokoro.New(kokoro.Config{BaseURL: "http://kokoro:8880/", Voice: voice, HTTP: &http.Client{Transport: v}})
	if err != nil {
		t.Fatalf("new synth: %v", err)
	}
	return s
}

// The voice the kitchen speaks in is one the endpoint has, so it is healthy,
// and it was asked on the voice list rather than /health. A blend needs
// each of its voices.
//
// verifies SPEC §10, §13
func TestAnEndpointWithTheVoiceIsHealthy(t *testing.T) {
	for _, voice := range []string{"", "af_heart", "af_bella+af_sky", "af_bella(2)+af_sky(1)"} {
		v := &voicesAnswer{body: kokoroVoices}
		if err := synthOn(t, voice, v).Health(context.Background()); err != nil {
			t.Errorf("voice %q: health = %v, want nil", voice, err)
		}
		if v.asked != "GET http://kokoro:8880/v1/audio/voices" {
			t.Errorf("asked %q, want GET the voice list", v.asked)
		}
	}
}

// An endpoint without the voice answers every utterance 400; readiness says
// so at startup, naming the voice (ADR-0097).
//
// verifies SPEC §10, §13
func TestAnEndpointWithoutTheVoiceIsNotHealthy(t *testing.T) {
	err := synthOn(t, "af_bella+af_river", &voicesAnswer{body: kokoroVoices}).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), `no voice "af_river" among its 10`) {
		t.Errorf("health = %v, want af_river named", err)
	}
	err = synthOn(t, "", &voicesAnswer{status: http.StatusBadGateway, body: "upstream down"}).Health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("health = %v, want the 502's body", err)
	}
}
