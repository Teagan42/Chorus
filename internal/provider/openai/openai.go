// Package openai speaks and hears through any server with OpenAI's audio
// API: /v1/audio/speech for the voice and /v1/audio/transcriptions for the
// ear. OpenAI itself, Groq, LocalAI, whisper.cpp's server, openedai-speech
// and speaches' own TTS all answer the same two routes, so one client covers
// a household that already runs one of them (ADR-0081).
//
// Kokoro and speaches keep their own packages: their quirks are measured and
// pinned (ADR-0023, ADR-0024), and a generic client cannot assume them.
package openai

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds one request, as the pinned providers do: a clause
// and an utterance are both bounded work, and a hung server would otherwise
// park the goroutine waiting on it for the whole session.
const DefaultTimeout = 30 * time.Second

// errBody bounds how much of a failed response is quoted.
const errBody = 1 << 10

// endpoint joins a base URL and a /v1 route. The base may be given with or
// without its /v1, since providers document it both ways.
func endpoint(base, route string) (string, error) {
	if base == "" {
		return "", errors.New("base url is required")
	}
	base = strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1")
	return base + "/v1" + route, nil
}

// authorize sets the bearer token, when there is one. Never logged, and
// never quoted in an error.
func authorize(req *http.Request, key string) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
}

// failure quotes a non-200 response: the status alone does not separate an
// unknown model from an unknown voice from a bad key, and only the body says
// which.
func failure(what string, resp *http.Response) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
	return fmt.Errorf("%s: %s: %s", what, resp.Status, strings.TrimSpace(string(msg)))
}

// HealthURL is what readiness asks: the model list, which every server with
// this API serves and OpenAI answers only to a key it accepts.
func HealthURL(base string) string {
	u, _ := endpoint(base, "/models")
	return u
}
