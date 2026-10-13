package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/openai"
)

// openaiSpeech is complete() with both the voice and the ear on an
// OpenAI-compatible server, and Kokoro's and speaches' URLs gone.
func openaiSpeech() map[string]string {
	env := complete()
	delete(env, kokoroURLEnv)
	delete(env, sttURLEnv)
	env[ttsProviderEnv] = providerOpenAI
	env[openaiTTSURLEnv] = "https://api.openai.com"
	env[openaiTTSKeyEnv] = "sk-tts"
	env[openaiTTSModelEnv] = "gpt-4o-mini-tts"
	env[openaiTTSVoiceEnv] = "nova"
	env[sttProviderEnv] = providerOpenAI
	env[openaiSTTURLEnv] = "https://api.groq.com/openai/v1"
	env[openaiSTTKeyEnv] = "gsk-stt"
	env[openaiSTTModelEnv] = "whisper-large-v3-turbo"
	return env
}

// wyomingSpeech is the same on Home Assistant's Wyoming services.
func wyomingSpeech() map[string]string {
	env := complete()
	delete(env, kokoroURLEnv)
	delete(env, sttURLEnv)
	env[ttsProviderEnv] = providerWyoming
	env[wyomingTTSAddrEnv] = "tcp://piper:10200"
	env[wyomingTTSVoiceEnv] = "en_US-lessac-medium"
	env[sttProviderEnv] = providerWyoming
	env[wyomingSTTAddrEnv] = "whisper:10300"
	env[wyomingSTTModelEnv] = "faster-whisper/small-int8"
	return env
}

// Whichever voice and ear the house chose, every event says which they
// were, as the journal records Kokoro and speaches (SPEC §8, ADR-0081).
//
// verifies SPEC §8
func TestEveryVoiceAndEarIsStamped(t *testing.T) {
	for name, tc := range map[string]struct {
		env      map[string]string
		stt, tts string
	}{
		"openai":  {openaiSpeech(), "whisper-large-v3-turbo", "gpt-4o-mini-tts/nova"},
		"wyoming": {wyomingSpeech(), "faster-whisper/small-int8", "en_US-lessac-medium"},
	} {
		cfg := configFromEnv(lookup(tc.env))
		if err := cfg.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		p, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler), nil)
		if err != nil {
			t.Errorf("%s: providers: %v", name, err)
			continue
		}
		if p.versions.STT != tc.stt || p.versions.TTS != tc.tts {
			t.Errorf("%s: versions STT=%q TTS=%q, want %q and %q", name, p.versions.STT, p.versions.TTS, tc.stt, tc.tts)
		}
	}
}

// Each provider's own variables are demanded when it is chosen, by name,
// and the defaults' are not.
//
// verifies SPEC §13
func TestValidateNamesTheChosenSpeechProvidersVariables(t *testing.T) {
	for name, tc := range map[string]struct {
		env       map[string]string
		want, not []string
	}{
		"openai": {
			env:  map[string]string{ttsProviderEnv: providerOpenAI, sttProviderEnv: providerOpenAI},
			want: []string{openaiTTSURLEnv, openaiTTSModelEnv, openaiTTSVoiceEnv, openaiSTTURLEnv, openaiSTTModelEnv},
			not:  []string{kokoroURLEnv, sttURLEnv, openaiTTSKeyEnv, openaiSTTKeyEnv},
		},
		"wyoming": {
			env:  map[string]string{ttsProviderEnv: providerWyoming, sttProviderEnv: providerWyoming},
			want: []string{wyomingTTSAddrEnv, wyomingTTSVoiceEnv, wyomingSTTAddrEnv, wyomingSTTModelEnv},
			not:  []string{kokoroURLEnv, sttURLEnv},
		},
		"unknown": {
			env:  map[string]string{ttsProviderEnv: "piper", sttProviderEnv: "whisper"},
			want: []string{`"piper" is not a voice`, `"whisper" is not an ear`},
		},
	} {
		err := configFromEnv(lookup(tc.env)).validate()
		if err == nil {
			t.Errorf("%s: validated", name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: error does not name %s: %v", name, w, err)
			}
		}
		for _, n := range tc.not {
			// As a line's name: STT_URL is also the tail of OPENAI_STT_URL.
			if strings.Contains(err.Error(), "  "+n+" ") {
				t.Errorf("%s: error demands %s: %v", name, n, err)
			}
		}
	}
}

func TestValidateRefusesAWyomingAddressWithoutAPort(t *testing.T) {
	env := wyomingSpeech()
	env[wyomingSTTAddrEnv] = "whisper"
	err := configFromEnv(lookup(env)).validate()
	if err == nil || !strings.Contains(err.Error(), wyomingSTTAddrEnv) {
		t.Errorf("err = %v, want it to name %s", err, wyomingSTTAddrEnv)
	}
}

// Readiness asks the chosen servers, the way each answers: the model list
// with the key for OpenAI's API, describe for Wyoming.
func TestReadinessAsksTheChosenSpeechServers(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+" "+r.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	env := openaiSpeech()
	env[openaiTTSURLEnv] = srv.URL
	env[openaiSTTURLEnv] = srv.URL + "/v1"
	cfg := configFromEnv(lookup(env))
	if err := voiceProbe(cfg, srv.Client())(context.Background()); err != nil {
		t.Errorf("voice: %v", err)
	}
	if err := earProbe(cfg, srv.Client())(context.Background()); err != nil {
		t.Errorf("ear: %v", err)
	}
	if want := []string{"/v1/models Bearer sk-tts", "/v1/models Bearer gsk-stt"}; strings.Join(asked, ",") != strings.Join(want, ",") {
		t.Errorf("asked %q, want %q", asked, want)
	}
	if openai.HealthURL(env[openaiSTTURLEnv]) != srv.URL+"/v1/models" {
		t.Error("the ear's health url is not its model list")
	}

	// Nothing serves Wyoming here, so the probe must fail rather than pass
	// by asking some HTTP route.
	w := configFromEnv(lookup(wyomingSpeech()))
	w.WyomingSTTAddr, w.WyomingTTSAddr = "127.0.0.1:1", "127.0.0.1:1"
	if err := earProbe(w, srv.Client())(context.Background()); err == nil {
		t.Error("the Wyoming ear's probe passed with nothing listening")
	}
	if err := voiceProbe(w, srv.Client())(context.Background()); err == nil {
		t.Error("the Wyoming voice's probe passed with nothing listening")
	}
}
