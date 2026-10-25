package main

import (
	"bufio"
	"bytes"
	"errors"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/capability"
)

// runMainEnv makes this test binary chorusd itself, so a test can start
// the daemon as a household does and read what it says on its way out.
const runMainEnv = "CHORUSD_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// chooses is complete() with the household's choice of engine, voice and
// ear, each with its own variables and none of the defaults' it replaced.
func chooses(llm, tts, ear string) map[string]string {
	env := complete()
	pick := func(from map[string]string, prefixes ...string) {
		for k, v := range from {
			for _, p := range prefixes {
				if strings.HasPrefix(k, p) {
					env[k] = v
				}
			}
		}
	}
	if llm == providerVLLM {
		env = onVLLM()
	}
	switch tts {
	case providerOpenAI:
		pick(openaiSpeech(), "OPENAI_TTS_")
	case providerWyoming:
		pick(wyomingSpeech(), "WYOMING_TTS_")
	}
	if tts != providerKokoro {
		delete(env, kokoroURLEnv)
		env[ttsProviderEnv] = tts
	}
	switch ear {
	case providerOpenAI:
		pick(openaiSpeech(), "OPENAI_STT_")
	case providerWyoming:
		pick(wyomingSpeech(), "WYOMING_STT_")
	}
	if ear != providerSpeaches {
		delete(env, sttURLEnv)
		env[sttProviderEnv] = ear
	}
	return env
}

// declared is the pipeline's providers as role/name, sorted.
func declared(p capability.Pipeline) []string {
	var out []string
	for _, d := range p.Providers {
		out = append(out, string(d.Role)+"/"+d.Name)
	}
	slices.Sort(out)
	return out
}

// Every engine, voice and ear chorusd can be told to run is checked against
// what the architecture needs before a satellite is heard, and every one
// runs: Ollama says what it costs, and nothing else has anything to say
// (SPEC §10, ADR-0102).
//
// verifies SPEC §10
func TestEveryChoiceOfEngineVoiceAndEarIsCheckedAndRuns(t *testing.T) {
	for _, llm := range []string{providerOllama, providerVLLM} {
		for _, tts := range []string{providerKokoro, providerOpenAI, providerWyoming} {
			for _, ear := range []string{providerSpeaches, providerOpenAI, providerWyoming} {
				name := llm + "+" + tts + "+" + ear
				cfg := configFromEnv(lookup(chooses(llm, tts, ear)))
				if err := cfg.validate(); err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				var logs bytes.Buffer
				p, err := buildProviders(cfg, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
				if err != nil {
					t.Errorf("%s refused: %v", name, err)
					continue
				}
				want := []string{"embedding/ollama", "endpointing/smartturn", "llm/" + llm, "speaker_id/speakerid", "stt/" + ear, "tts/" + tts}
				slices.Sort(want)
				if got := declared(p.pipeline); !slices.Equal(got, want) {
					t.Errorf("%s: checked %v, want what was built: %v", name, got, want)
				}
				warned := strings.Contains(logs.String(), "llm provider ollama lacks streams_tool_arguments")
				if warned != (llm == providerOllama) {
					t.Errorf("%s: Ollama's cost said = %v, want %v:\n%s", name, warned, llm == providerOllama, logs.String())
				}
			}
		}
	}
}

// Alice's household speaks German. Parakeet hears English only and ignores
// the language it is sent, so on speaches every German turn would be heard
// as English words nobody said: chorusd refuses to start, and says which
// ear does hear German. On their Wyoming whisper the same language runs.
//
// verifies SPEC §10
func TestAGermanHouseholdIsRefusedParakeetAndRunsOnWhisper(t *testing.T) {
	env := complete()
	env[sttLanguageEnv] = "de"
	_, err := buildProviders(configFromEnv(lookup(env)), nil, slog.New(slog.DiscardHandler), nil)
	if err == nil {
		t.Fatal("chorusd started with Parakeet asked to hear German")
	}
	for _, want := range []string{`stt provider speaches hears en, not "de"`, "words nobody said", "CHORUS_STT_PROVIDER=wyoming"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not say %q: %v", want, err)
		}
	}

	env = chooses(providerOllama, providerKokoro, providerWyoming)
	env[sttLanguageEnv] = "de"
	if _, err := buildProviders(configFromEnv(lookup(env)), nil, slog.New(slog.DiscardHandler), nil); err != nil {
		t.Errorf("German on a Wyoming whisper was refused: %v", err)
	}
}

// shippedEnv is the environment a fresh clone's chorusd container starts
// with: .env.example's set lines, under docker-compose.yml's own.
func shippedEnv(t *testing.T) map[string]string {
	t.Helper()
	env := map[string]string{}
	f, err := os.Open(filepath.Join("..", "..", ".env.example"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf(".env.example: %q is not NAME=value", line)
		}
		env[k] = v
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "docker-compose.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var compose struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(raw, &compose); err != nil {
		t.Fatalf("docker-compose.yml: %v", err)
	}
	chorusd, ok := compose.Services["chorusd"]
	if !ok {
		t.Fatal("docker-compose.yml has no chorusd service")
	}
	maps.Copy(env, chorusd.Environment)
	return env
}

// The stack the repository ships is held to the same check on every push,
// so a schema change that would refuse it fails here, not in a house
// (ADR-0102).
//
// verifies SPEC §10, §13
func TestTheShippedStackPassesTheCheck(t *testing.T) {
	cfg := configFromEnv(lookup(shippedEnv(t)))
	if err := cfg.validate(); err != nil {
		t.Fatalf("the shipped environment does not validate: %v", err)
	}
	var logs bytes.Buffer
	p, err := buildProviders(cfg, nil, slog.New(slog.NewTextHandler(&logs, nil)), nil)
	if err != nil {
		t.Fatalf("the shipped stack was refused: %v", err)
	}
	want := []string{"endpointing/smartturn", "llm/ollama", "speaker_id/speakerid", "stt/speaches", "tts/kokoro"}
	if got := declared(p.pipeline); !slices.Equal(got, want) {
		t.Errorf("checked %v, want %v", got, want)
	}
	if !strings.Contains(logs.String(), "llm provider ollama lacks streams_tool_arguments") {
		t.Errorf("the shipped stack runs Ollama without saying what it costs:\n%s", logs.String())
	}
}

// startChorusd runs chorusd as a household starts it, with nothing in its
// environment but env, and returns what it wrote to stderr once it exits.
func startChorusd(t *testing.T, env map[string]string) (stderr string, exit int) {
	t.Helper()
	dir := t.TempDir()
	devices := filepath.Join(dir, "devices.yaml")
	inv := "satellites:\n  - name: satellite1-a1b2c3\n    address: 10.0.0.11:6053\n    psk: " + goodPSK + "\n    room: kitchen\n    profile: satellite1\n"
	if err := os.WriteFile(devices, []byte(inv), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-devices", devices)
	cmd.Env = []string{runMainEnv + "=1"}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var out bytes.Buffer
	cmd.Stderr = &out
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &ee):
		return out.String(), ee.ExitCode()
	}
	t.Fatalf("start chorusd: %v", err)
	return "", 0
}

// chorusdEnv is the least a real start needs, on loopback and with no
// ops listener. The journal's DSN does not parse, so a daemon that gets
// past its providers stops at the journal without dialling anything.
func chorusdEnv(t *testing.T) map[string]string {
	return map[string]string{
		blobDirEnv:     t.TempDir(),
		listenEnv:      "127.0.0.1:0",
		opsListenEnv:   opsOff,
		ollamaURLEnv:   "http://10.0.0.20:11434",
		ollamaModelEnv: "qwen3:32b",
		kokoroURLEnv:   "http://10.0.0.20:8880",
		sttURLEnv:      "http://10.0.0.20:8000",
		journal.DSNEnv: "postgres://chorus@[::1",
	}
}

// The whole start, as the binary runs it: a German household on Parakeet
// is told why at startup and the daemon exits before it opens the journal
// or listens for a satellite.
//
// verifies SPEC §10
func TestChorusdRefusesAPipelineThatCannotWorkBeforeItListens(t *testing.T) {
	env := chorusdEnv(t)
	env[sttLanguageEnv] = "de"
	stderr, exit := startChorusd(t, env)
	if exit != 1 {
		t.Fatalf("exit = %d, want 1:\n%s", exit, stderr)
	}
	for _, want := range []string{"cannot run this pipeline", `stt provider speaches hears en, not "de"`, "docs/reference/providers.md"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}
	for _, never := range []string{"connect journal", "chorusd listening"} {
		if strings.Contains(stderr, never) {
			t.Errorf("a refused pipeline went on to %q:\n%s", never, stderr)
		}
	}
}

// The default stack gets past the check, says what Ollama costs it on the
// way, and goes on to open the journal.
//
// verifies SPEC §10
func TestChorusdSaysWhatItsEngineCostsAndStartsAnyway(t *testing.T) {
	stderr, exit := startChorusd(t, chorusdEnv(t))
	if exit != 1 || !strings.Contains(stderr, "connect journal") {
		t.Fatalf("exit = %d, want the journal's DSN to stop it after the check:\n%s", exit, stderr)
	}
	warn := strings.Index(stderr, "llm provider ollama lacks streams_tool_arguments")
	if warn < 0 || warn > strings.Index(stderr, "connect journal") {
		t.Errorf("Ollama's cost is not said before the journal opens:\n%s", stderr)
	}
}
