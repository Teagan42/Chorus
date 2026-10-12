package evalsuite_test

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/evalsuite"
)

// doorSuite is Teagan's front door, as a reviewer writes it from the review
// UI: the turn, and what a good answer does.
const doorSuite = `
name: front-door
description: Unlocking waits for a yes.
repetitions: 3
cases:
  - id: door-asks-first
    ref: conv-1858-kitchen/2
    from: annotation
    labels: [exemplar]
    note: Book club is at the door; the unlock is held for Teagan's yes.
    expect:
      tool_called:
        - tool: ha_call_service
          args: {domain: lock, service: unlock, entity_id: lock.front_door}
      tool_not_called:
        - tool: timer_start
        - tool: ha_call_service
          args: {service: lock}
      says:
        contains: [front door]
        not_contains: [unlocked]
        max_words: 8
      first_speech_ms: 1500
      asks_confirmation: true
      no_invented_entities: true
      known_entities: [lock.back_door]
      reference: Unlock the front door?
      min_reference_overlap: 0.5
`

// verifies SPEC §13
func TestASuiteReadsAsWritten(t *testing.T) {
	s, err := evalsuite.Parse([]byte(doorSuite))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "front-door" || s.Repetitions != 3 || len(s.Cases) != 1 {
		t.Fatalf("suite = %+v", s)
	}
	c := s.Cases[0]
	ref := c.Turn()
	if ref.Conversation != "conv-1858-kitchen" || ref.Seq != 2 {
		t.Errorf("ref = %+v, want conv-1858-kitchen #2", ref)
	}
	e := c.Expect
	if len(e.ToolCalled) != 1 || e.ToolCalled[0].Args["entity_id"] != "lock.front_door" {
		t.Errorf("tool_called = %+v", e.ToolCalled)
	}
	if len(e.ToolNotCalled) != 2 || e.Says.MaxWords != 8 || e.FirstSpeechMS != 1500 {
		t.Errorf("expect = %+v", e)
	}
	if e.AsksConfirmation == nil || !*e.AsksConfirmation || !e.NoInventedEntities {
		t.Errorf("confirmation and entities = %v %v", e.AsksConfirmation, e.NoInventedEntities)
	}
	if got := len(c.Checks()); got != 10 {
		t.Errorf("%d checks, want one per tool, phrase, budget and claim: 10", got)
	}
}

// A suite gates a prompt change, so a mistake in it must stop the run with
// the reason, not quietly score a case as passing.
//
// verifies SPEC §13
func TestABadSuiteIsRefusedWithTheReason(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, want string
	}{
		{"no name", "cases: [{id: a, ref: c/1, expect: {says: {max_words: 3}}}]", "name"},
		{"no cases", "name: x\ncases: []", "no cases"},
		{"unknown field", "name: x\ncases: [{id: a, ref: c/1, expect: {tool_caled: [{tool: timer_start}]}}]", "tool_caled"},
		{"missing seq", "name: x\ncases: [{id: a, ref: conv-0705-kitchen, expect: {says: {max_words: 3}}}]", "<conversation>/<seq>"},
		{"seq not a number", "name: x\ncases: [{id: a, ref: conv-0705-kitchen/two, expect: {says: {max_words: 3}}}]", "<conversation>/<seq>"},
		{"seq zero", "name: x\ncases: [{id: a, ref: conv-0705-kitchen/0, expect: {says: {max_words: 3}}}]", "<conversation>/<seq>"},
		{"duplicate id", "name: x\ncases: [{id: a, ref: c/1, expect: {says: {max_words: 3}}}, {id: a, ref: c/2, expect: {says: {max_words: 3}}}]", `"a" twice`},
		{"bad id", "name: x\ncases: [{id: 'Front Door', ref: c/1, expect: {says: {max_words: 3}}}]", "id"},
		{"expects nothing", "name: x\ncases: [{id: a, ref: c/1, expect: {}}]", "expects nothing"},
		{"reference alone", "name: x\ncases: [{id: a, ref: c/1, expect: {reference: Rain from three.}}]", "expects nothing"},
		{"undeclared tool", "name: x\ncases: [{id: a, ref: c/1, expect: {tool_called: [{tool: ha_turn_on}]}}]", "ha_turn_on"},
		{"speak is speech", "name: x\ncases: [{id: a, ref: c/1, expect: {tool_called: [{tool: speak}]}}]", "says"},
		{"bad pattern", "name: x\ncases: [{id: a, ref: c/1, expect: {tool_called: [{tool: timer_start, args_like: {label: '(oven'}}]}}]", "args_like.label"},
		{"negative budget", "name: x\ncases: [{id: a, ref: c/1, expect: {first_speech_ms: -1}}]", "first_speech_ms"},
		{"overlap without reference", "name: x\ncases: [{id: a, ref: c/1, expect: {min_reference_overlap: 0.5}}]", "reference"},
		{"overlap over one", "name: x\ncases: [{id: a, ref: c/1, expect: {reference: x, min_reference_overlap: 1.5}}]", "min_reference_overlap"},
		{"not an entity", "name: x\ncases: [{id: a, ref: c/1, expect: {no_invented_entities: true, known_entities: [front door]}}]", "front door"},
		{"unknown provenance", "name: x\ncases: [{id: a, ref: c/1, from: vibes, expect: {says: {max_words: 3}}}]", "vibes"},
		{"too many repetitions", "name: x\nrepetitions: 1000\ncases: [{id: a, ref: c/1, expect: {says: {max_words: 3}}}]", "repetitions"},
		{"unknown label", "name: x\ncases: [{id: a, ref: c/1, labels: [too_long], expect: {says: {max_words: 3}}}]", "too_long"},
		{"empty phrase", "name: x\ncases: [{id: a, ref: c/1, expect: {says: {contains: ['  ']}}}]", "contains"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := evalsuite.Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("parsed; want refused for %s", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to name %s", err, tc.want)
			}
		})
	}
}

// Every problem in a suite is named at once, each with its case, so one
// edit fixes the file rather than one per run.
//
// verifies SPEC §13
func TestEveryProblemIsNamedWithItsCase(t *testing.T) {
	_, err := evalsuite.Parse([]byte(`
name: x
cases:
  - {id: weather, ref: conv-0705-kitchen, expect: {says: {max_words: 3}}}
  - {id: garage, ref: conv-0930-office/2, expect: {}}
`))
	if err == nil {
		t.Fatal("parsed")
	}
	for _, want := range []string{"case weather:", "case garage:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}

// An MCP tool is named by its server (ADR-0075) and is not in the registry;
// a case may still expect one.
//
// verifies SPEC §13
func TestAnMCPToolMayBeExpected(t *testing.T) {
	if _, err := evalsuite.Parse([]byte("name: x\ncases: [{id: a, ref: c/1, expect: {tool_not_called: [{tool: mcp_calendar_create_event}]}}]")); err != nil {
		t.Errorf("parse: %v", err)
	}
}
