package evalsuite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/session"
)

// Config is what one engine is built to run under.
type Config struct {
	Model  string
	Prompt string
	Tools  map[string]registry.ToolSpec

	// Seed is the repetition being asked. A provider that samples by seed
	// may pass it on; Ollama's engine takes none, so its runs vary freely.
	Seed int
}

// Engines builds turn engines: the review UI's engine factory, with a seed.
// A provider slots in by building one (cmd/chorus-eval).
type Engines interface {
	Engine(cfg Config) (session.Engine, journal.Versions, error)
}

// EngineFunc adapts a function to Engines.
type EngineFunc func(cfg Config) (session.Engine, journal.Versions, error)

// Engine calls f.
func (f EngineFunc) Engine(cfg Config) (session.Engine, journal.Versions, error) { return f(cfg) }

// Factory adapts an engine factory in the review UI's shape, which takes no
// seed (cmd/reviewui/replay.go).
func Factory(f func(model, prompt string, tools map[string]registry.ToolSpec) (session.Engine, journal.Versions, error)) Engines {
	return EngineFunc(func(cfg Config) (session.Engine, journal.Versions, error) {
		return f(cfg.Model, cfg.Prompt, cfg.Tools)
	})
}

// Side is one half of a comparison. An empty Model, Prompt or Tools takes
// the one each case's turn recorded, so the zero Side asks every turn again
// under what it ran under.
type Side struct {
	// Name is how a report calls the side: baseline or candidate.
	Name string

	// Recorded scores the take the journal recorded instead of asking, once.
	Recorded bool

	Model  string
	Prompt string
	Tools  map[string]registry.ToolSpec

	// HouseTools adds to Tools each MCP tool a case's turn was offered that
	// Tools does not name. A candidate built from the registry cannot
	// declare the house's servers' tools, and asking a turn without the
	// tools it had scores the missing server, not the candidate (ADR-0101).
	HouseTools bool
}

// Run is one ask of one case.
type Run struct {
	Seed     int
	Versions journal.Versions
	Observation

	// Cached is a run whose take came from the cache, not the model.
	Cached bool
}

// Runner asks cases of an engine.
type Runner struct {
	Engines Engines

	// Provider names the engines in the cache key: two providers may serve
	// a model by the same name.
	Provider string

	// Cache keeps takes by case, context, versions and seed, so scoring
	// again asks nothing. Nil keeps nothing.
	Cache Cache

	// Clock times the first speech. Nil times nothing.
	Clock journal.Clock

	// Repetitions is how often each case is asked; at least one.
	Repetitions int

	// Timeout bounds one ask. Zero leaves it to ctx.
	Timeout time.Duration
}

// Side asks every case that is not skipped, Repetitions times each, under
// side, and returns each case's runs by its id. An engine that cannot be
// built or asked stops the run: that is a broken setup, not a regression.
func (r Runner) Side(ctx context.Context, cases []Resolved, side Side) (map[string][]Run, error) {
	out := map[string][]Run{}
	for _, c := range cases {
		if c.Skipped != "" {
			continue
		}
		if side.Recorded {
			out[c.Case.ID] = []Run{{Versions: c.Turn.Versions, Observation: Observation{Take: c.Turn.Recorded}}}
			continue
		}
		cfg := Config{Model: side.Model, Prompt: side.Prompt, Tools: side.Tools}
		if cfg.Model == "" {
			cfg.Model = c.Recorded.Versions.Model
		}
		if cfg.Prompt == "" {
			cfg.Prompt = c.Recorded.Prompt
		}
		switch {
		case cfg.Tools == nil:
			cfg.Tools = c.Recorded.Tools
		case side.HouseTools:
			cfg.Tools = registry.WithProvided(cfg.Tools, c.Recorded.Tools)
		}
		for seed := range max(r.Repetitions, 1) {
			cfg.Seed = seed
			run, err := r.ask(ctx, c, cfg)
			if err != nil {
				return nil, fmt.Errorf("%s: case %s (%s), run %d: %w", side.Name, c.Case.ID, c.Case.Turn(), seed+1, err)
			}
			out[c.Case.ID] = append(out[c.Case.ID], run)
		}
	}
	return out, nil
}

func (r Runner) ask(ctx context.Context, c Resolved, cfg Config) (Run, error) {
	eng, v, err := r.Engines.Engine(cfg)
	if err != nil {
		return Run{}, fmt.Errorf("build engine: %w", err)
	}
	run := Run{Seed: cfg.Seed, Versions: v}
	key := r.key(c, cfg, v)
	if r.Cache != nil {
		e, ok, err := r.Cache.Get(key)
		if err != nil {
			return Run{}, err
		}
		if ok {
			run.Observation = Observation{Take: e.Take, FirstSpeech: time.Duration(e.FirstSpeechNS), Measured: e.Measured}
			run.Cached = true
			return run, nil
		}
	}
	if r.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.Timeout)
		defer cancel()
	}
	t := &timed{eng: eng, clock: r.Clock}
	take, err := rerun.Ask(ctx, t, c.Input)
	var failed *rerun.Failed
	switch {
	case errors.As(err, &failed):
		// A broken stream may be the model or the wire, so it is scored
		// as a failed run and never cached.
		run.Observation = Observation{Take: take, Err: failed.Error()}
		return run, nil
	case err != nil:
		return Run{}, err
	}
	run.Observation = Observation{Take: take, FirstSpeech: t.first, Measured: t.measured}
	if r.Cache != nil {
		if err := r.Cache.Put(key, Entry{Versions: v, Take: take, FirstSpeechNS: int64(t.first), Measured: t.measured}); err != nil {
			return Run{}, err
		}
	}
	return run, nil
}

// key names a take by everything that could change it: the provider, the
// turn and what it is asked, what the engine runs under, by version and by
// text, and the seed.
func (r Runner) key(c Resolved, cfg Config, v journal.Versions) string {
	in, _ := json.Marshal(c.Input)
	b, _ := json.Marshal(struct {
		Provider, Ref, Input, Model, Prompt, Tools string
		Versions                                   journal.Versions
		Seed                                       int
	}{
		Provider: r.Provider, Ref: c.Case.Turn().String(), Input: digest(in), Model: cfg.Model,
		Prompt: digest([]byte(cfg.Prompt)), Tools: digest([]byte(ollama.ToolSchema(cfg.Tools))),
		Versions: v, Seed: cfg.Seed,
	})
	return digest(b)
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// timed is an engine whose first speech is timed from the ask.
type timed struct {
	eng      session.Engine
	clock    journal.Clock
	first    time.Duration
	measured bool
}

func (t *timed) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	if t.clock == nil {
		return t.eng.Turn(ctx, in)
	}
	start := t.clock.Now()
	actions, err := t.eng.Turn(ctx, in)
	if err != nil {
		return nil, err
	}
	t.measured = true
	out := make(chan session.Action)
	// Ends when the engine closes its stream, which it does on cancel; Ask
	// reads out to the end, so no send is ever stranded.
	go func() {
		defer close(out)
		spoke := false
		for a := range actions {
			if d, ok := a.(session.SpeechDelta); ok && !spoke && d.Text != "" {
				spoke, t.first = true, t.clock.Now().Sub(start)
			}
			out <- a
		}
	}()
	return out, nil
}
