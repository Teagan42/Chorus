//go:build models

// Model tier: needs a reachable Ollama endpoint. Run with `task test:models`,
// pointing it at one:
//
//	go test -tags=models ./internal/evalsuite/ -ollama-url https://host -v
//
// Without it these skip. What is checked is the half the scripted model
// cannot reach: that a real model answers every case of the seed suite
// through the runner, with its first speech timed, and that a second
// scoring comes from the cache. How well it answers is printed, not
// asserted: that is what a suite is for.
package evalsuite_test

import (
	"bytes"
	"context"
	"flag"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

var (
	endpoint  = flag.String("ollama-url", "", "Ollama base URL; skips when empty")
	realModel = flag.String("ollama-model", "qwen3:14b", "model to exercise")
)

// verifies SPEC §11, §13
func TestARealModelAnswersTheSeedSuite(t *testing.T) {
	if *endpoint == "" {
		t.Skip("no -ollama-url")
	}
	s, err := evalsuite.Load(seedSuite)
	if err != nil {
		t.Fatal(err)
	}
	store := day(t)
	rs, err := evalsuite.Resolve(context.Background(), store, daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	engines := evalsuite.Factory(func(model, prompt string, tools map[string]registry.ToolSpec) (session.Engine, journal.Versions, error) {
		e, err := ollama.New(ollama.Config{BaseURL: *endpoint, Model: model, Prompt: prompt, Specs: tools})
		if err != nil {
			return nil, journal.Versions{}, err
		}
		return e, e.Versions(), nil
	})
	r := evalsuite.Runner{
		Engines: engines, Provider: "ollama", Clock: wall{}, Cache: &evalsuite.MemCache{},
		Repetitions: 2, Timeout: 3 * time.Minute,
	}
	baseline := evalsuite.Side{Name: "baseline", Model: *realModel}
	cand := evalsuite.Side{Name: "candidate", Model: *realModel, Prompt: ollama.DefaultPrompt, Tools: registry.Specs}
	base, err := r.Side(context.Background(), rs, baseline)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Side(context.Background(), rs, cand)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rs {
		if len(got[c.Case.ID]) != 2 {
			t.Errorf("%s: %d candidate runs, want 2", c.Case.ID, len(got[c.Case.ID]))
		}
		for _, run := range got[c.Case.ID] {
			if !run.Measured {
				t.Errorf("%s seed %d: first speech not timed", c.Case.ID, run.Seed)
			}
		}
	}
	again, err := r.Side(context.Background(), rs, cand)
	if err != nil {
		t.Fatal(err)
	}
	for id, runs := range again {
		for _, run := range runs {
			if !run.Cached && run.Err == "" {
				t.Errorf("%s seed %d was asked again", id, run.Seed)
			}
		}
	}
	var out bytes.Buffer
	_ = evalsuite.Compare(s, rs, baseline, cand, base, got, evalsuite.DefaultGate).WriteText(&out)
	t.Log("\n" + out.String())
}

type wall struct{}

func (wall) Now() time.Time { return time.Now() }
