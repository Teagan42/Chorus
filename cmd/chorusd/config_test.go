package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/identity"
	"github.com/teaganglenn/chorus/internal/journal"
)

// complete is every variable the daemon reads, set. Tests unset from here.
func complete() map[string]string {
	return map[string]string{
		journal.DSNEnv:  "postgres://chorus:secret@db:5432/chorus",
		listenEnv:       "0.0.0.0:6055",
		blobDirEnv:      "/var/lib/chorus/blobs",
		ollamaURLEnv:    "http://ollama:11434",
		ollamaModelEnv:  "qwen3:32b",
		kokoroURLEnv:    "http://kokoro:8880",
		sttURLEnv:       "http://speaches:8000",
		speakerIDURLEnv: "http://speakerid:8890",
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
	for _, name := range []string{speakerIDURLEnv, hass.URLEnv, hass.TokenEnv} {
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
// one: without speaker identification everyone is a guest and no barge-in
// can pass the gate; without Home Assistant the declared ha_* tools answer
// not_implemented, which the model sees (SPEC §5, §7).
//
// verifies SPEC §5
func TestOptionalSidecarsDegradeWithALogLine(t *testing.T) {
	env := complete()
	delete(env, speakerIDURLEnv)
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
	for _, want := range []string{"guest", "barge-in", "not_implemented"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not say %q:\n%s", want, logs.String())
		}
	}
	if p.versions.Model != "qwen3:32b" || p.versions.Prompt == "" || p.versions.ToolSchema == "" {
		t.Errorf("versions = %+v, want the engine's", p.versions)
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
	for _, name := range []string{"ha_call_service", "ha_get_state", "ha_find_entities"} {
		if _, ok := p.tools[name]; !ok {
			t.Errorf("%s not wired", name)
		}
	}
	for _, off := range []string{"speaker identification is off", "home assistant is off"} {
		if strings.Contains(logs.String(), off) {
			t.Errorf("a fully configured daemon logged %q:\n%s", off, logs.String())
		}
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
