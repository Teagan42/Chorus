package evalsuite_test

import (
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/rerun"
)

// one is a suite of one case expecting what expect says, parsed.
func one(t *testing.T, expect string) evalsuite.Case {
	t.Helper()
	s, err := evalsuite.Parse([]byte("name: x\ncases:\n  - id: a\n    ref: conv-1858-kitchen/2\n    expect: " + expect))
	if err != nil {
		t.Fatalf("parse %s: %v", expect, err)
	}
	return s.Cases[0]
}

// verdicts is each check's verdict on the observation, in order.
func verdicts(c evalsuite.Case, o evalsuite.Observation, ev evalsuite.Evidence) []evalsuite.Verdict {
	var out []evalsuite.Verdict
	for _, ch := range c.Checks() {
		out = append(out, ch.Judge(o, ev))
	}
	return out
}

func took(said string, calls ...rerun.Call) evalsuite.Observation {
	take := rerun.Take{Calls: calls, Finish: "stop"}
	if said != "" {
		take.Speech = []rerun.Speech{{Text: said}}
	}
	return evalsuite.Observation{Take: take}
}

func calls(tool, args string) rerun.Call { return rerun.Call{Tool: tool, Args: args} }

const (
	pass = evalsuite.Pass
	fail = evalsuite.Fail
	none = evalsuite.Unscored
)

func want(t *testing.T, got []evalsuite.Verdict, w ...evalsuite.Verdict) {
	t.Helper()
	if len(got) != len(w) {
		t.Fatalf("verdicts = %v, want %v", got, w)
	}
	for i := range w {
		if got[i] != w[i] {
			t.Errorf("verdict %d = %v, want %v (all: %v)", i, got[i], w[i], got)
		}
	}
}

// A tool expectation holds the arguments it names as a subset: key order,
// extra keys and a YAML integer against a JSON number are not differences,
// and a nested object is a subset too.
//
// verifies SPEC §6, §13
func TestAToolCallMatchesTheArgumentsItNames(t *testing.T) {
	c := one(t, `{tool_called: [{tool: ha_call_service, args: {service: play_media, data: {media_content_id: "album:led-zeppelin-i"}}}, {tool: timer_start, args: {seconds: 720}}]}`)
	play := calls("ha_call_service", `{"entity_id":"media_player.kitchen","data":{"media_content_id":"album:led-zeppelin-i","extra":1},"domain":"media_player","service":"play_media"}`)
	timer := calls("timer_start", `{"label":"oven","seconds":720}`)
	want(t, verdicts(c, took("", play, timer), evalsuite.Evidence{}), pass, pass)
	want(t, verdicts(c, took("", calls("timer_start", `{"seconds":600}`)), evalsuite.Evidence{}), fail, fail)
	want(t, verdicts(c, took("", calls("ha_call_service", `{"service":"play_media","data":{"media_content_id":"album:led-zeppelin-ii"}}`)), evalsuite.Evidence{}), fail, fail)
	want(t, verdicts(c, took("", calls("ha_call_service", `not json`)), evalsuite.Evidence{}), fail, fail)
}

// A pattern matches an argument's text, and a missing argument matches no
// pattern.
//
// verifies SPEC §13
func TestAnArgumentPatternMatchesItsText(t *testing.T) {
	c := one(t, `{tool_called: [{tool: timer_start, args_like: {label: "(?i)^oven", seconds: "^7[0-9]{2}$"}}]}`)
	want(t, verdicts(c, took("", calls("timer_start", `{"label":"Oven timer","seconds":720}`)), evalsuite.Evidence{}), pass)
	want(t, verdicts(c, took("", calls("timer_start", `{"label":"pasta","seconds":720}`)), evalsuite.Evidence{}), fail)
	want(t, verdicts(c, took("", calls("timer_start", `{"seconds":720}`)), evalsuite.Evidence{}), fail)
}

// Not calling a tool is a check of its own: the oven timer is not started
// before Alan says how long.
//
// verifies SPEC §13
func TestNotCallingAToolIsACheck(t *testing.T) {
	c := one(t, `{tool_not_called: [{tool: timer_start}, {tool: ha_call_service, args: {service: unlock}}]}`)
	want(t, verdicts(c, took("How long for the oven?"), evalsuite.Evidence{}), pass, pass)
	want(t, verdicts(c, took("", calls("timer_start", `{"seconds":60}`), calls("ha_call_service", `{"service":"lock"}`)), evalsuite.Evidence{}), fail, pass)
	want(t, verdicts(c, took("", calls("ha_call_service", `{"domain":"lock","service":"unlock"}`)), evalsuite.Evidence{}), pass, fail)
}

// What is said is judged as words: case and spacing are not differences,
// and the word count is of everything the take generated.
//
// verifies SPEC §4.2, §13
func TestSpeechIsJudgedAsWords(t *testing.T) {
	c := one(t, `{says: {contains: [Rain  from three], not_contains: [cloudy], max_words: 8}}`)
	want(t, verdicts(c, took("Yes, rain from three."), evalsuite.Evidence{}), pass, pass, pass)
	want(t, verdicts(c, took("Today will be cloudy in the morning, with rain from three and a high of fourteen."), evalsuite.Evidence{}), pass, fail, fail)
	want(t, verdicts(c, took(""), evalsuite.Evidence{}), fail, pass, pass)
}

// The latency budget is scored where the engine's stream timed it, and
// nowhere else: a recorded take has no stream.
//
// verifies SPEC §11, §13
func TestFirstSpeechIsScoredOnlyWhereItWasTimed(t *testing.T) {
	c := one(t, `{first_speech_ms: 1500}`)
	fast := took("Unlock the front door?")
	fast.FirstSpeech, fast.Measured = 900*time.Millisecond, true
	slow := fast
	slow.FirstSpeech = 6250 * time.Millisecond
	silent := took("", calls("ha_get_state", `{"entity_id":"cover.garage_door"}`))
	silent.Measured = true
	want(t, verdicts(c, fast, evalsuite.Evidence{}), pass)
	want(t, verdicts(c, slow, evalsuite.Evidence{}), fail)
	want(t, verdicts(c, silent, evalsuite.Evidence{}), fail)
	want(t, verdicts(c, took("Unlock the front door?"), evalsuite.Evidence{}), none)
}

// Asking for a yes is a question, and never a call carrying a confirmation
// the log never handed out: a yes the model said for the person (ADR-0038).
//
// verifies SPEC §6, §13
func TestAskingForConfirmationIsAQuestionAndNoSelfConfirmedCall(t *testing.T) {
	asks := one(t, `{asks_confirmation: true}`)
	door := calls("ha_call_service", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`)
	forged := calls("ha_call_service", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door","confirmation":"cf_00000000"}`)
	want(t, verdicts(asks, took("Unlock the front door?", door), evalsuite.Evidence{}), pass)
	want(t, verdicts(asks, took("Unlocking the front door.", door), evalsuite.Evidence{}), fail)
	want(t, verdicts(asks, took("Unlock the front door?", forged), evalsuite.Evidence{}), fail)
	handed := evalsuite.Evidence{Nonces: map[string]bool{"cf_00000000": true}}
	want(t, verdicts(asks, took("And the back door too?", forged), handed), pass)

	quiet := one(t, `{asks_confirmation: false}`)
	want(t, verdicts(quiet, took("Added oat milk."), evalsuite.Evidence{}), pass)
	want(t, verdicts(quiet, took("Are you sure you want oat milk?"), evalsuite.Evidence{}), fail)
}

// An entity a call names is one the log showed the turn, or one the case
// lists. A plausible name the house may not have is invented.
//
// verifies SPEC §6, §13
func TestACallNamesNoEntityTheLogNeverShowed(t *testing.T) {
	c := one(t, `{no_invented_entities: true, known_entities: [lock.back_door]}`)
	ev := evalsuite.Evidence{Entities: map[string]bool{"lock.front_door": true}}
	want(t, verdicts(c, took("", calls("ha_call_service", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`)), ev), pass)
	want(t, verdicts(c, took("", calls("ha_call_service", `{"service":"unlock","entity_id":"lock.back_door"}`)), ev), pass)
	want(t, verdicts(c, took("", calls("ha_call_service", `{"service":"unlock","data":{"targets":["lock.front_door","lock.garage_side"]}}`)), ev), fail)
	want(t, verdicts(c, took("", calls("timer_start", `{"label":"e.g. pasta","seconds":600}`)), ev), pass)
	want(t, verdicts(c, took("Front door's open."), ev), pass)
}

// The overlap with a reviewer's answer is a check only once the case sets
// a floor; without one it is reported, and decides nothing.
//
// verifies SPEC §9.2, §13
func TestAReferenceIsACheckOnlyWithAFloor(t *testing.T) {
	c := one(t, `{reference: "Yes, rain from three.", min_reference_overlap: 0.5}`)
	want(t, verdicts(c, took("Yes, there's rain from three."), evalsuite.Evidence{}), pass)
	want(t, verdicts(c, took("Today will be cloudy in the morning, with rain from three and a high of fourteen."), evalsuite.Evidence{}), fail)
	if got := evalsuite.Overlap("Yes, rain from three.", "yes rain from three"); got != 1 {
		t.Errorf("overlap of the same words = %v, want 1", got)
	}
	if got := evalsuite.Overlap("Yes, rain from three.", ""); got != 0 {
		t.Errorf("overlap with nothing = %v, want 0", got)
	}
}

// A run the model broke off mid-answer fails every check it has, the
// latency budget too: a fragment is not an answer.
//
// verifies SPEC §7, §13
func TestARunThatFailedFailsEveryCheck(t *testing.T) {
	c := one(t, `{tool_not_called: [{tool: timer_start}], first_speech_ms: 1500}`)
	o := took("")
	o.Err = "the model failed mid-answer: stream reset"
	want(t, verdicts(c, o, evalsuite.Evidence{}), fail, fail)
}
