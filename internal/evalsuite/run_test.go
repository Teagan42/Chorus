package evalsuite_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/evalsuite/evalfake"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/session"
)

// brief is the persona change under test: the household asked for shorter
// answers.
const brief = "You are Eddie. Answer in one short sentence."

// candidate is brief on today's registry.
var candidate = evalsuite.Side{Name: "candidate", Prompt: brief, Tools: registry.Offered()}

// weatherSuite is Teagan's forecast, which she cut off as too long.
const weatherSuite = `
name: weather
cases:
  - id: weather-brief
    ref: conv-0705-kitchen/2
    from: barge-in
    expect:
      tool_called: [{tool: ha_get_state, args: {entity_id: weather.home}}]
      says: {contains: [rain], max_words: 12}
      first_speech_ms: 1000
`

func model(t *testing.T, store journal.Store) *evalfake.Model {
	t.Helper()
	m, err := evalfake.New(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func resolved(t *testing.T, store journal.Store, yaml string) (evalsuite.Suite, []evalsuite.Resolved) {
	t.Helper()
	s := parse(t, yaml)
	rs, err := evalsuite.Resolve(context.Background(), store, daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	return s, rs
}

func answer(said string, first time.Duration, calls ...rerun.Call) evalfake.Answer {
	return evalfake.Answer{Take: rerun.Take{Speech: []rerun.Speech{{Text: said}}, Calls: calls}, FirstSpeech: first}
}

var weatherCall = rerun.Call{Tool: "ha_get_state", Args: `{"entity_id":"weather.home"}`}

// A sampled model answers differently each time. Asked three times, each
// check says how many runs passed, so one lucky run cannot pass a case and
// one unlucky run cannot fail it.
//
// verifies SPEC §13
func TestRepetitionsAreScoredAsAPassRate(t *testing.T) {
	store := day(t)
	m := model(t, store)
	m.Scripts = map[string]map[string][]evalfake.Answer{brief: {"what's the weather today": {
		answer("Rain from three, high of fourteen.", 600*time.Millisecond, weatherCall),
		answer("Today will be cloudy in the morning, with rain from three and a high of fourteen.", 600*time.Millisecond, weatherCall),
		answer("Rain from three.", 1400*time.Millisecond, weatherCall),
	}}}
	s, rs := resolved(t, store, weatherSuite)
	r := evalsuite.Runner{Engines: m, Clock: m.Clock, Repetitions: 3}
	runs, err := r.Side(context.Background(), rs, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(runs["weather-brief"]); got != 3 {
		t.Fatalf("%d runs, want 3", got)
	}
	rep := evalsuite.Compare(s, rs, evalsuite.Side{Name: "baseline", Recorded: true}, candidate,
		map[string][]evalsuite.Run{"weather-brief": runs["weather-brief"][:1]}, runs, evalsuite.DefaultGate)
	rates := map[string]string{}
	for _, ch := range rep.Cases[0].Checks {
		rates[ch.Name] = ch.Candidate.String()
	}
	want := map[string]string{
		`calls ha_get_state {"entity_id":"weather.home"}`: "3/3",
		`says "rain"`:           "3/3",
		"says at most 12 words": "2/3",
		"speaks within 1000 ms": "2/3",
	}
	for name, w := range want {
		if rates[name] != w {
			t.Errorf("%s = %s, want %s (all: %v)", name, rates[name], w, rates)
		}
	}
	if rep.Candidate.Flaky != 2 || rep.Candidate.Runs != 3 {
		t.Errorf("candidate = %+v, want two flaky checks over three runs", rep.Candidate)
	}
}

// First speech is timed from the ask to the first words on the engine's
// stream, on the runner's clock.
//
// verifies SPEC §11, §13
func TestFirstSpeechIsTimedFromTheEnginesStream(t *testing.T) {
	store := day(t)
	m := model(t, store)
	m.Latency = 750 * time.Millisecond
	_, rs := resolved(t, store, weatherSuite)
	runs, err := evalsuite.Runner{Engines: m, Clock: m.Clock}.Side(context.Background(), rs, evalsuite.Side{Name: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	run := runs["weather-brief"][0]
	if !run.Measured || run.FirstSpeech != 750*time.Millisecond {
		t.Errorf("first speech = %v (measured %v), want 750ms", run.FirstSpeech, run.Measured)
	}
	untimed, err := evalsuite.Runner{Engines: m}.Side(context.Background(), rs, evalsuite.Side{Name: "baseline"})
	if err != nil {
		t.Fatal(err)
	}
	if untimed["weather-brief"][0].Measured {
		t.Errorf("a runner with no clock measured first speech")
	}
}

// The baseline is the turn asked again under what it recorded: the model,
// the prompt's text and the tools as the wire offered them, by label.
//
// verifies SPEC §13
func TestTheBaselineAsksUnderTheRecordedContext(t *testing.T) {
	store := day(t)
	m := model(t, store)
	_, rs := resolved(t, store, weatherSuite)
	if _, err := (evalsuite.Runner{Engines: m}).Side(context.Background(), rs, evalsuite.Side{Name: "baseline"}); err != nil {
		t.Fatal(err)
	}
	asked := m.Asked()[0]
	if asked.Model != "qwen3-32b@1" || asked.Prompt != ollama.DefaultPrompt || ollama.ToolSchema(asked.Tools) != ollama.ToolSchema(registry.Specs) {
		t.Errorf("baseline asked %s with %d tools, want the day's model, sys@3 and tools@7", asked.Model, len(asked.Tools))
	}
}

// Scoring again asks nothing: a take is kept by case, context, versions and
// seed, so only what changed is asked.
//
// verifies SPEC §13
func TestATakeIsCachedByCaseVersionsAndSeed(t *testing.T) {
	store := day(t)
	m := model(t, store)
	_, rs := resolved(t, store, weatherSuite)
	cache := &evalsuite.MemCache{}
	r := evalsuite.Runner{Engines: m, Cache: cache, Clock: m.Clock, Provider: "fake", Repetitions: 2}
	first, err := r.Side(context.Background(), rs, candidate)
	if err != nil {
		t.Fatal(err)
	}
	again, err := r.Side(context.Background(), rs, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(m.Asked()); n != 2 {
		t.Errorf("asked %d times over two scorings of two runs, want 2", n)
	}
	for i, run := range again["weather-brief"] {
		if !run.Cached || run.Take.Said() != first["weather-brief"][i].Take.Said() || run.FirstSpeech != first["weather-brief"][i].FirstSpeech {
			t.Errorf("run %d again = %+v, want the cached take of %+v", i, run, first["weather-brief"][i])
		}
	}

	r.Repetitions = 3
	if _, err := r.Side(context.Background(), rs, candidate); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Asked()); n != 3 {
		t.Errorf("asked %d times after adding a third seed, want only it: 3", n)
	}
	edited := candidate
	edited.Prompt = brief + " Never say the temperature."
	if _, err := r.Side(context.Background(), rs, edited); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Asked()); n != 6 {
		t.Errorf("asked %d times after editing the prompt, want three more: 6", n)
	}
	r.Provider = "another"
	if _, err := r.Side(context.Background(), rs, edited); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Asked()); n != 9 {
		t.Errorf("asked %d times under another provider, want three more: 9", n)
	}
}

// The cache on disk outlives the process: a second runner scores from it.
//
// verifies SPEC §13
func TestTheCacheOnDiskOutlivesTheRunner(t *testing.T) {
	store := day(t)
	m := model(t, store)
	_, rs := resolved(t, store, weatherSuite)
	dir := t.TempDir()
	for range 2 {
		r := evalsuite.Runner{Engines: m, Cache: evalsuite.DirCache{Dir: dir}, Clock: m.Clock}
		if _, err := r.Side(context.Background(), rs, candidate); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(m.Asked()); n != 1 {
		t.Errorf("asked %d times, want once", n)
	}

	// A torn file is asked again, not trusted.
	torn, err := filepath.Glob(filepath.Join(dir, "*", "*.json"))
	if err != nil || len(torn) != 1 {
		t.Fatalf("cache holds %v (%v), want one file", torn, err)
	}
	if err := os.WriteFile(torn[0], []byte(`{"take":`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := evalsuite.Runner{Engines: m, Cache: evalsuite.DirCache{Dir: dir}, Clock: m.Clock}
	if _, err := r.Side(context.Background(), rs, candidate); err != nil {
		t.Fatal(err)
	}
	if n := len(m.Asked()); n != 2 {
		t.Errorf("asked %d times after the file tore, want again: 2", n)
	}
}

// A model that breaks off mid-answer is a failed run, scored and reported,
// never cached: asking again may answer.
//
// verifies SPEC §7, §13
func TestARunTheModelBrokeOffFailsAndIsNotCached(t *testing.T) {
	store := day(t)
	m := model(t, store)
	broken := answer("Rain fr", 0)
	broken.Fail = "stream reset"
	m.Scripts = map[string]map[string][]evalfake.Answer{brief: {"what's the weather today": {broken}}}
	_, rs := resolved(t, store, weatherSuite)
	r := evalsuite.Runner{Engines: m, Cache: &evalsuite.MemCache{}}
	for range 2 {
		runs, err := r.Side(context.Background(), rs, candidate)
		if err != nil {
			t.Fatal(err)
		}
		if run := runs["weather-brief"][0]; run.Cached || !strings.Contains(run.Err, "stream reset") {
			t.Errorf("run = %+v, want a failed, uncached run", run)
		}
	}
	if n := len(m.Asked()); n != 2 {
		t.Errorf("asked %d times, want each time: 2", n)
	}
}

// An engine that cannot be asked at all is a broken setup, not a candidate
// that regressed: the run stops and says which case it was on.
//
// verifies SPEC §13
func TestAnEngineThatCannotBeAskedStopsTheRun(t *testing.T) {
	store := day(t)
	_, rs := resolved(t, store, weatherSuite)
	down := evalsuite.EngineFunc(func(evalsuite.Config) (session.Engine, journal.Versions, error) {
		return refusing{}, journal.Versions{Model: "qwen3:32b"}, nil
	})
	_, err := evalsuite.Runner{Engines: down}.Side(context.Background(), rs, candidate)
	if err == nil || !strings.Contains(err.Error(), "case weather-brief (conv-0705-kitchen/2)") || !errors.Is(err, errRefused) {
		t.Errorf("err = %v, want the case named and the engine's error", err)
	}
}

var errRefused = errors.New("ollama chat: dial tcp 10.0.0.20:11434: connect: connection refused")

type refusing struct{}

func (refusing) Turn(context.Context, session.Input) (<-chan session.Action, error) {
	return nil, errRefused
}

// A skipped case is not asked, and the recorded side is the turn's own
// take, once, with nothing timed.
//
// verifies SPEC §13
func TestSkippedCasesAreNotAskedAndTheRecordedSideAsksNothing(t *testing.T) {
	store := day(t)
	m := model(t, store)
	_, rs := resolved(t, store, weatherSuite)
	rs[0].Skipped = "context_missing: tool_schema"
	runs, err := evalsuite.Runner{Engines: m}.Side(context.Background(), rs, candidate)
	if err != nil || len(runs) != 0 || len(m.Asked()) != 0 {
		t.Errorf("runs = %v, err = %v, asked %d: want a skipped case left alone", runs, err, len(m.Asked()))
	}
	rs[0].Skipped = ""
	runs, err = evalsuite.Runner{Engines: m, Repetitions: 5}.Side(context.Background(), rs, evalsuite.Side{Name: "baseline", Recorded: true})
	if err != nil {
		t.Fatal(err)
	}
	got := runs["weather-brief"]
	if len(got) != 1 || got[0].Measured || got[0].Take.Said() != rs[0].Turn.Recorded.Said() || len(m.Asked()) != 0 {
		t.Errorf("recorded runs = %+v, want the recorded take once, untimed, nothing asked", got)
	}
}

// The review UI's engine factory slots in as it is.
//
// verifies SPEC §12
func TestTheReviewUIsFactoryShapeSlotsIn(t *testing.T) {
	var got string
	f := evalsuite.Factory(func(model, prompt string, tools map[string]registry.ToolSpec) (session.Engine, journal.Versions, error) {
		got = model + "|" + prompt
		return refusing{}, journal.Versions{Model: model}, nil
	})
	if _, v, err := f.Engine(evalsuite.Config{Model: "qwen3:32b", Prompt: brief, Seed: 4}); err != nil || v.Model != "qwen3:32b" || got != "qwen3:32b|"+brief {
		t.Errorf("factory built %q, %+v, %v", got, v, err)
	}
}
