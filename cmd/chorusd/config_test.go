package main

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/provider/kokoro"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/speaches"
	"github.com/teagan42/chorus/internal/registry"
)

// complete is every variable the daemon reads, set. Tests unset from here.
func complete() map[string]string {
	return map[string]string{
		journal.DSNEnv:  "postgres://chorus:secret@db:5432/chorus",
		listenEnv:       "0.0.0.0:6055",
		blobDirEnv:      "/var/lib/chorus/blobs",
		ollamaURLEnv:    "http://ollama:11434",
		ollamaModelEnv:  "qwen3:32b",
		ollamaEmbedEnv:  "nomic-embed-text",
		kokoroURLEnv:    "http://kokoro:8880",
		sttURLEnv:       "http://speaches:8000",
		speakerIDURLEnv: "http://speakerid:8890",
		smartTurnURLEnv: "http://smartturn:8891",
		hass.URLEnv:     "http://homeassistant:8123",
		hass.TokenEnv:   "token",
	}
}

func lookup(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

// Startup is the one place a missing variable can be reported with its
// name and purpose; a daemon that finds out on the first turn reports a
// dial error instead. Every required variable is named in one error, so one
// restart fixes them all (SPEC §13).
//
// verifies SPEC §13
func TestValidateNamesEveryMissingVariable(t *testing.T) {
	cfg := configFromEnv(lookup(map[string]string{}))
	err := cfg.validate()
	if err == nil {
		t.Fatal("an empty environment validated")
	}
	for _, name := range []string{blobDirEnv, ollamaURLEnv, ollamaModelEnv, kokoroURLEnv, sttURLEnv} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	// Optional ones are not demanded.
	for _, name := range []string{speakerIDURLEnv, smartTurnURLEnv, hass.URLEnv, hass.TokenEnv} {
		if strings.Contains(err.Error(), name) {
			t.Errorf("error demands optional %s: %v", name, err)
		}
	}
}

func TestValidateAcceptsACompleteEnvironment(t *testing.T) {
	if err := configFromEnv(lookup(complete())).validate(); err != nil {
		t.Errorf("complete environment rejected: %v", err)
	}
}

// The listen address and the journal DSN have defaults the rest of the repo
// already agrees on: the port the firmware dials, and the compose database.
func TestDefaultsMatchTheRepo(t *testing.T) {
	cfg := configFromEnv(lookup(map[string]string{}))
	if cfg.Listen != defaultListen {
		t.Errorf("Listen = %q, want %q", cfg.Listen, defaultListen)
	}
	if cfg.DSN != journal.DefaultDSN {
		t.Errorf("DSN = %q, want the package default", cfg.DSN)
	}
}

// Half a Home Assistant configuration is a mistake, not a choice: name the
// missing half rather than silently running without the tools.
func TestValidateRefusesHalfAHomeAssistant(t *testing.T) {
	for set, missing := range map[string]string{hass.URLEnv: hass.TokenEnv, hass.TokenEnv: hass.URLEnv} {
		env := complete()
		delete(env, missing)
		err := configFromEnv(lookup(env)).validate()
		if err == nil || !strings.Contains(err.Error(), missing) {
			t.Errorf("%s alone: err = %v, want it to name %s", set, err, missing)
		}
	}
}

// An absent sidecar is a degraded mode, not a failure, but never a silent
// one: without speaker identification everyone is a guest and barge-in
// gates on energy and words alone, so the television can interrupt; without
// Home Assistant the declared ha_* tools answer not_implemented, which the
// model sees; without Smart Turn every turn waits out 800 ms of quiet
// (SPEC §4.5, §5, §7, ADR-0031, ADR-0036).
//
// verifies SPEC §5, §4.5
func TestOptionalSidecarsDegradeWithALogLine(t *testing.T) {
	env := complete()
	delete(env, speakerIDURLEnv)
	delete(env, smartTurnURLEnv)
	delete(env, ollamaEmbedEnv)
	delete(env, hass.URLEnv)
	delete(env, hass.TokenEnv)
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	p, err := buildProviders(configFromEnv(lookup(env)), nil, log)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if p.speakers != nil {
		t.Error("a resolver was built with no speaker-ID endpoint")
	}
	if len(p.tools) != 0 {
		t.Errorf("tools = %v, want none wired", p.tools)
	}
	if p.judge != nil || p.endpointer() != nil {
		t.Error("a semantic endpointer was built with no Smart Turn endpoint")
	}
	if p.embedder != nil {
		t.Error("an embedder was built with no embedding model")
	}
	for _, want := range []string{"guest", "barge-in", "television", "not_implemented", "800 ms of quiet", "mid-sentence", "memory relevance is off", "newest 20 memories"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not say %q:\n%s", want, logs.String())
		}
	}
	// The line says what barge-in does now, not what it once could not.
	if strings.Contains(logs.String(), "no barge-in") {
		t.Errorf("log still says barge-in is impossible:\n%s", logs.String())
	}
	if p.versions.Model != "qwen3:32b" || p.versions.Prompt == "" || p.versions.ToolSchema == "" {
		t.Errorf("versions = %+v, want the engine's", p.versions)
	}
}

// Every event the daemon records must say which ear heard it and which
// voice spoke it, not only which model answered: a replay or an eval
// otherwise cannot tell two sidecar builds' transcripts apart (SPEC §8).
//
// verifies SPEC §8
func TestProvidersStampTheEarAndTheVoice(t *testing.T) {
	p, err := buildProviders(configFromEnv(lookup(complete())), nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if p.versions.STT != speaches.DefaultModel {
		t.Errorf("STT = %q, want the transcriber's model %q", p.versions.STT, speaches.DefaultModel)
	}
	if want := kokoro.DefaultModel + "/" + kokoro.DefaultVoice; p.versions.TTS != want {
		t.Errorf("TTS = %q, want the synth's model and voice %q", p.versions.TTS, want)
	}
}

// With everything configured the sidecars are wired and the household the
// gate knows is the enrolled one.
func TestFullyConfiguredProvidersWireEverything(t *testing.T) {
	ids := &identity.Identities{Model: "nemo_en_titanet_large", Dim: 192}
	var logs bytes.Buffer
	p, err := buildProviders(configFromEnv(lookup(complete())), ids, slog.New(slog.NewTextHandler(&logs, nil)))
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	if p.speakers == nil {
		t.Error("no resolver with a speaker-ID endpoint and a household")
	}
	ep, ok := p.endpointer().(*listen.Semantic)
	if !ok {
		t.Fatalf("endpointer is %T with a Smart Turn endpoint, want a semantic one", p.endpointer())
	}
	if d, ok := ep.Judge.(listen.Dangling); !ok || d.Judge != p.judge || d.Words != p.stt {
		t.Errorf("judge is %#v, want Smart Turn checked against the turn's own transcriber", ep.Judge)
	}
	if a, b := p.endpointer(), p.endpointer(); a == b {
		t.Error("two links share one endpointer, and with it one turn in progress")
	}
	for _, name := range []string{"ha_call_service", "ha_get_state", "ha_find_entities"} {
		if _, ok := p.tools[name]; !ok {
			t.Errorf("%s not wired", name)
		}
	}
	if p.embedder == nil || p.embedder.EmbedModel() != "nomic-embed-text" {
		t.Errorf("embedder = %v, want nomic-embed-text on the turn engine's endpoint", p.embedder)
	}
	for _, off := range []string{"speaker identification is off", "semantic endpointing is off", "home assistant is off", "memory relevance is off"} {
		if strings.Contains(logs.String(), off) {
			t.Errorf("a fully configured daemon logged %q:\n%s", off, logs.String())
		}
	}
}

// The model is offered only what this daemon runs. With everything
// configured, every tool it is shown has an executor, the supervisor's own
// speak and end_session aside, and the tool schema the journal records is
// that set's: media_search, which nothing runs, is not in it (ADR-0060).
//
// verifies SPEC §6, §8, §14
func TestEveryToolTheModelIsOfferedHasAnExecutor(t *testing.T) {
	cfg := configFromEnv(lookup(complete()))
	p, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.tools = p.tools
		d.Memories = memory.NewMemStore()
	})
	// A link is served only once the daemon has composed its tools.
	r.join(t, kitchenIP)

	if _, ok := registry.Offered()["media_search"]; ok {
		t.Error("media_search is offered to the model, though nothing runs it")
	}
	for name := range registry.Offered() {
		if name == "speak" || name == "end_session" {
			continue
		}
		if _, ok := r.dm.tools[name]; !ok {
			t.Errorf("%s is offered to the model, but nothing runs it", name)
		}
	}
	offered, err := ollama.New(ollama.Config{BaseURL: cfg.OllamaURL, Model: cfg.OllamaModel, Specs: registry.Offered()})
	if err != nil {
		t.Fatal(err)
	}
	if p.versions.ToolSchema != offered.Versions().ToolSchema {
		t.Errorf("tool schema = %s, want the offered set's %s", p.versions.ToolSchema, offered.Versions().ToolSchema)
	}
}

// The gate's speaker stage runs exactly when something identifies speakers:
// the mode follows SPEAKERID_URL, never an empty id at candidate time, so an
// unidentified voice with the sidecar up is still the television (ADR-0031).
//
// verifies SPEC §4.3
func TestBargeInGateSkipsTheSpeakerStageOnlyWithoutSpeakerID(t *testing.T) {
	ids := &identity.Identities{Model: "nemo_en_titanet_large", Dim: 192}
	voice := make([]float32, ids.Dim)
	voice[0] = 1
	if err := ids.Enroll("alan", "Alan", [][]float32{voice, voice, voice}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	log := slog.New(slog.DiscardHandler)

	with, err := buildProviders(configFromEnv(lookup(complete())), ids, log)
	if err != nil {
		t.Fatalf("providers with speaker id: %v", err)
	}
	gate := with.bargeInGate()
	if gate.SpeakerIDUnavailable {
		t.Error("the speaker stage is skipped with " + speakerIDURLEnv + " set")
	}
	if !reflect.DeepEqual(gate.Household, []string{"alan"}) {
		t.Errorf("household = %q, want the enrolled one", gate.Household)
	}

	env := complete()
	delete(env, speakerIDURLEnv)
	without, err := buildProviders(configFromEnv(lookup(env)), ids, log)
	if err != nil {
		t.Fatalf("providers without speaker id: %v", err)
	}
	if gate := without.bargeInGate(); !gate.SpeakerIDUnavailable {
		t.Error("the speaker stage runs with " + speakerIDURLEnv + " unset, so nothing could barge in")
	}
	if with.bargeInGate().MinEnergy != without.bargeInGate().MinEnergy || with.bargeInGate().MinWords != without.bargeInGate().MinWords {
		t.Error("the other stages changed with the speaker mode; only the speaker stage may")
	}
}

// A household enrolled against one model must not be scored by another:
// the mismatch is a startup error, not weeks of guests (ADR-0025).
func TestProvidersRefuseAHouseholdFromAnotherEmbedder(t *testing.T) {
	ids := &identity.Identities{Model: "some_other_model", Dim: 192}
	_, err := buildProviders(configFromEnv(lookup(complete())), ids, slog.New(slog.DiscardHandler))
	if err == nil || !strings.Contains(err.Error(), "some_other_model") {
		t.Errorf("err = %v, want the enrolled model named", err)
	}
}
