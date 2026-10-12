package evalsuite_test

import (
	"context"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/evalsuite/evalfake"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
)

// seedSuite is the checked-in suite over the household's Thursday.
const seedSuite = "../../eval/household.yaml"

// thursday runs the seed suite: the baseline side as given, the candidate
// under prompt and tools, answering from script where it has one.
func thursday(t *testing.T, baseline, cand evalsuite.Side, script map[string][]evalfake.Answer) evalsuite.Report {
	t.Helper()
	s, err := evalsuite.Load(seedSuite)
	if err != nil {
		t.Fatal(err)
	}
	store := day(t)
	rs, err := evalsuite.Resolve(context.Background(), store, daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	m := model(t, store)
	m.Latency = 600 * time.Millisecond
	m.Scripts = map[string]map[string][]evalfake.Answer{cand.Prompt: script}
	r := evalsuite.Runner{Engines: m, Clock: m.Clock, Repetitions: s.Repetitions, Cache: &evalsuite.MemCache{}}
	base, err := r.Side(context.Background(), rs, baseline)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.Side(context.Background(), rs, cand)
	if err != nil {
		t.Fatal(err)
	}
	return evalsuite.Compare(s, rs, baseline, cand, base, got, evalsuite.DefaultGate)
}

func changes(rep evalsuite.Report) map[string]string {
	out := map[string]string{}
	for _, c := range rep.Cases {
		for _, ch := range c.Checks {
			if ch.Change != "" {
				out[c.ID+": "+ch.Name] = ch.Change
			}
		}
	}
	return out
}

// Every case of the seed suite names a turn of the day, and every turn's
// context resolves, so nothing is skipped.
//
// verifies SPEC §13
func TestTheSeedSuiteResolvesAgainstTheHouseholdsDay(t *testing.T) {
	s, err := evalsuite.Load(seedSuite)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := evalsuite.Resolve(context.Background(), day(t), daysContexts(), s)
	if err != nil {
		t.Fatal(err)
	}
	if len(rs) != len(s.Cases) || len(rs) < 6 {
		t.Fatalf("%d of %d cases resolved", len(rs), len(s.Cases))
	}
	for _, r := range rs {
		if r.Skipped != "" {
			t.Errorf("%s skipped: %s", r.Case.ID, r.Skipped)
		}
	}
}

// Today's build against what the day recorded: the registry has deferred
// media_search since (ADR-0060), so Alice's album search regresses, and
// the gate says so. Nothing else moves.
//
// verifies SPEC §13
func TestTodaysRegistryRegressesTheMusicSearch(t *testing.T) {
	rep := thursday(t,
		evalsuite.Side{Name: "baseline", Recorded: true},
		evalsuite.Side{Name: "candidate", Prompt: ollama.DefaultPrompt, Tools: registry.Offered()},
		nil)
	want := map[string]string{`zeppelin-searches: calls media_search like {"query":"(?i)zeppelin"}`: "regressed"}
	got := changes(rep)
	if len(got) != len(want) || got[`zeppelin-searches: calls media_search like {"query":"(?i)zeppelin"}`] != "regressed" {
		t.Errorf("changes = %v, want only %v", got, want)
	}
	if !rep.Regressed {
		t.Errorf("the gate passed a candidate that cannot search for music")
	}
}

// The persona change the household wanted: shorter answers. On the day's
// tools, against the day's context asked again, it fixes the forecast and
// breaks nothing, so the gate lets it through.
//
// verifies SPEC §13
func TestABriefPersonaFixesTheForecastAndPasses(t *testing.T) {
	rep := thursday(t,
		evalsuite.Side{Name: "baseline"},
		evalsuite.Side{Name: "candidate", Prompt: brief, Tools: registry.Specs},
		map[string][]evalfake.Answer{"what's the weather today": {answer("Rain from three, high of fourteen.", 0, weatherCall)}})
	if rep.Regressed {
		t.Fatalf("regressed: %v", rep.Why)
	}
	if got := changes(rep); got["weather-brief: says at most 12 words"] != "improved" || len(got) != 1 {
		t.Errorf("changes = %v, want the forecast's word count improved", got)
	}
	if rep.Score.Delta <= 0 || rep.Skipped != 0 {
		t.Errorf("score = %+v, skipped %d", rep.Score, rep.Skipped)
	}
}

// The same persona, had it also stopped asking before unlocking: the gate
// fails it on the door, however much shorter the forecast got.
//
// verifies SPEC §6, §13
func TestABriefPersonaThatStopsAskingFailsTheGate(t *testing.T) {
	rep := thursday(t,
		evalsuite.Side{Name: "baseline"},
		evalsuite.Side{Name: "candidate", Prompt: brief, Tools: registry.Specs},
		map[string][]evalfake.Answer{
			"what's the weather today": {answer("Rain from three, high of fourteen.", 0, weatherCall)},
			"unlock the front door":    {answer("Unlocked.", 0, unlock)},
			"yes":                      {answer("Unlock the front door?", 0, rerun.Call{Tool: "ha_call_service", Args: `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`})},
		})
	got := changes(rep)
	for _, want := range []string{"door-asks-first: asks for a yes", "door-yes-redeems: asks no question"} {
		if got[want] != "regressed" {
			t.Errorf("%s = %q, want regressed (all: %v)", want, got[want], got)
		}
	}
	if !rep.Regressed {
		t.Errorf("the gate passed a persona that unlocks without asking")
	}
}
