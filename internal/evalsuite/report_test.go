package evalsuite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/evalsuite/evalfake"
	"github.com/teagan42/chorus/internal/rerun"
)

// doorAndList is two turns the household got right, and one it did not.
const doorAndList = `
name: door-and-list
cases:
  - id: door-asks-first
    ref: conv-1858-kitchen/2
    expect:
      tool_called: [{tool: ha_call_service, args: {service: unlock, entity_id: lock.front_door}}]
      asks_confirmation: true
      no_invented_entities: true
  - id: oat-milk
    ref: conv-2104-office/2
    expect:
      tool_called: [{tool: ha_call_service, args: {domain: todo, service: add_item}}]
      says: {max_words: 6}
      asks_confirmation: false
  - id: weather-brief
    ref: conv-0705-kitchen/2
    expect:
      says: {contains: [rain], max_words: 12}
`

// compare runs both sides of doorAndList, the candidate answering from
// script, and compares them under g.
func compare(t *testing.T, script map[string][]evalfake.Answer, reps int, g evalsuite.Gate) evalsuite.Report {
	t.Helper()
	store := day(t)
	m := model(t, store)
	m.Scripts = map[string]map[string][]evalfake.Answer{brief: script}
	s, rs := resolved(t, store, doorAndList)
	r := evalsuite.Runner{Engines: m, Clock: m.Clock, Repetitions: reps}
	baseline := evalsuite.Side{Name: "baseline"}
	base, err := r.Side(context.Background(), rs, baseline)
	if err != nil {
		t.Fatal(err)
	}
	cand, err := r.Side(context.Background(), rs, candidate)
	if err != nil {
		t.Fatal(err)
	}
	return evalsuite.Compare(s, rs, baseline, candidate, base, cand, g)
}

var unlock = rerun.Call{Tool: "ha_call_service", Args: `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`}

// A candidate that answers as the household's turns did passes the gate;
// one that is shorter where it was too long improves.
//
// verifies SPEC §13
func TestACandidateThatHoldsItsGroundPasses(t *testing.T) {
	rep := compare(t, map[string][]evalfake.Answer{
		"what's the weather today": {answer("Rain from three, high of fourteen.", 0, weatherCall)},
	}, 1, evalsuite.DefaultGate)
	if rep.Regressed {
		t.Errorf("regressed: %v", rep.Why)
	}
	if rep.Improvements != 1 || rep.Regressions != 0 || rep.Score.Delta <= 0 {
		t.Errorf("report = %d improved, %d regressed, Δ %v; want the weather's word count improved", rep.Improvements, rep.Regressions, rep.Score.Delta)
	}
}

// A candidate that unlocks the door without asking regresses the case that
// says it must ask, and the gate fails it.
//
// verifies SPEC §6, §13
func TestACandidateThatStopsAskingForAYesFailsTheGate(t *testing.T) {
	rep := compare(t, map[string][]evalfake.Answer{
		"unlock the front door": {answer("Front door unlocked.", 0, unlock)},
	}, 1, evalsuite.DefaultGate)
	if !rep.Regressed || rep.Regressions != 1 {
		t.Fatalf("report = regressed %v with %d regressions (%v), want the door's question regressed", rep.Regressed, rep.Regressions, rep.Why)
	}
	var door evalsuite.CaseReport
	for _, c := range rep.Cases {
		if c.ID == "door-asks-first" {
			door = c
		}
	}
	for _, ch := range door.Checks {
		if (ch.Name == "asks for a yes") != (ch.Change == "regressed") {
			t.Errorf("%s: %s", ch.Name, ch.Change)
		}
	}
	if !strings.Contains(rep.Verdict(), "1 check regressed, more than the 0 allowed") {
		t.Errorf("verdict = %s", rep.Verdict())
	}
}

// The gate has two knobs: regressions allowed, and how far the score may
// fall. Either alone fails a candidate.
//
// verifies SPEC §13
func TestTheGateFailsOnEitherKnob(t *testing.T) {
	unasked := map[string][]evalfake.Answer{"unlock the front door": {answer("Front door unlocked.", 0, unlock)}}
	loose := compare(t, unasked, 1, evalsuite.Gate{Threshold: 1, MaxRegressions: 1})
	if loose.Regressed {
		t.Errorf("a gate allowing one regression and any fall failed: %v", loose.Why)
	}
	strict := compare(t, unasked, 1, evalsuite.Gate{Threshold: 0, MaxRegressions: 1})
	if !strict.Regressed || !strings.Contains(strict.Verdict(), "the score fell by") {
		t.Errorf("a gate allowing no fall passed a score of %v against %v: %s", strict.Score.Candidate, strict.Score.Baseline, strict.Verdict())
	}
	same := compare(t, nil, 1, evalsuite.Gate{})
	if same.Regressed || same.Score.Delta != 0 {
		t.Errorf("the recorded answers against themselves: %s", same.Verdict())
	}
}

// One bad run in three is variance, not a regression: the pass rate falls
// by a third, which the score shows, and the check does not regress.
//
// verifies SPEC §13
func TestOneBadRunInThreeIsVarianceNotARegression(t *testing.T) {
	asks := answer("Unlock the front door?", 0, unlock)
	rep := compare(t, map[string][]evalfake.Answer{
		"unlock the front door": {asks, answer("Front door unlocked.", 0, unlock), asks},
	}, 3, evalsuite.Gate{Threshold: 0.1})
	if rep.Regressions != 0 || rep.Candidate.Flaky != 1 {
		t.Errorf("report = %d regressions, %d flaky; want none and one", rep.Regressions, rep.Candidate.Flaky)
	}
	if rep.Regressed {
		t.Errorf("regressed: %v", rep.Why)
	}
}

// A suite every case of which was skipped judges nothing, and a gate that
// judged nothing does not pass.
//
// verifies SPEC §13
func TestAGateThatComparedNothingFails(t *testing.T) {
	store := day(t)
	s, rs := resolved(t, store, weatherSuite)
	rs[0].Skipped = "context_missing: tool_schema"
	rep := evalsuite.Compare(s, rs, evalsuite.Side{Name: "baseline"}, candidate, nil, nil, evalsuite.DefaultGate)
	if !rep.Regressed || rep.Skipped != 1 || rep.Compared != 0 {
		t.Errorf("report = %+v, want a failed gate over one skipped case", rep)
	}
}

// The report reads three ways: a table for the terminal, Markdown for a pull
// request, and JSON for a machine, each with the verdict.
//
// verifies SPEC §13
func TestTheReportReadsInATerminalAPullRequestAndJSON(t *testing.T) {
	store := day(t)
	m := model(t, store)
	m.Latency = 400 * time.Millisecond
	m.Scripts = map[string]map[string][]evalfake.Answer{brief: {"unlock the front door": {answer("Front door unlocked.", 0, unlock)}}}
	s, rs := resolved(t, store, doorAndList+`
  - id: skipped
    ref: conv-1748-kitchen/10
    expect: {tool_called: [{tool: timer_cancel}]}
`)
	rs[3].Skipped = "context_missing: prompt"
	r := evalsuite.Runner{Engines: m, Clock: m.Clock}
	base, _ := r.Side(context.Background(), rs, evalsuite.Side{Name: "baseline"})
	cand, _ := r.Side(context.Background(), rs, candidate)
	rep := evalsuite.Compare(s, rs, evalsuite.Side{Name: "baseline"}, candidate, base, cand, evalsuite.DefaultGate)

	var text bytes.Buffer
	if err := rep.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"door-asks-first", "asks for a yes", "1/1", "0/1", "regressed", "skipped: context_missing: prompt", "REGRESSED"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, text.String())
		}
	}

	var md bytes.Buffer
	if err := rep.WriteMarkdown(&md); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Eval: door-and-list", "| `door-asks-first` | asks for a yes | 1/1 | 0/1 | regressed |", "`skipped` (conv-1748-kitchen/10) skipped: context_missing: prompt"} {
		if !strings.Contains(md.String(), want) {
			t.Errorf("markdown report lacks %q:\n%s", want, md.String())
		}
	}
	if strings.Contains(md.String(), "oat-milk") {
		t.Errorf("markdown lists a case nothing moved in:\n%s", md.String())
	}

	var js bytes.Buffer
	if err := rep.WriteJSON(&js); err != nil {
		t.Fatal(err)
	}
	var back evalsuite.Report
	if err := json.Unmarshal(js.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if !back.Regressed || back.Regressions != 1 || len(back.Cases) != 4 || back.Cases[0].Candidate[0].Said != "Front door unlocked." {
		t.Errorf("json report read back as %+v", back)
	}
	if ms := back.Cases[0].Candidate[0].FirstSpeechMS; ms == nil || *ms != 400 {
		t.Errorf("first speech in json = %v, want 400", ms)
	}
}
