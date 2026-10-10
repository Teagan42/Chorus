package session_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// garageState is Home Assistant's answer for the garage door this morning.
const garageState = `{"entity_id":"cover.garage_door","state":"open","attributes":{"friendly_name":"Garage Door","device_class":"garage"}}`

func reads(result string) session.Tool {
	return session.ToolFunc(func(context.Context, string) (string, error) { return result, nil })
}

// lastEntry is the newest step of the dialogue an ask carried.
func lastEntry(t *testing.T, in session.Input) journal.Entry {
	t.Helper()
	if len(in.Dialogue) == 0 {
		t.Fatal("the ask carried no dialogue")
	}
	return in.Dialogue[len(in.Dialogue)-1]
}

// Teagan asks from the kitchen whether the garage is shut. The model says it
// is checking and reads the cover; the answer is only worth reading if the
// model is asked again with it, before the turn is over.
//
// verifies SPEC §4.1
func TestAToolsResultIsAskedAboutBeforeTheTurnEnds(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Let me check.", Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{"ha_get_state": reads(garageState)})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s2", Text: "No, the garage door is open.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}

	s := r.open(t, "teagan")
	wait(t, heard(s, "is the garage door closed"))

	asks := r.engine.asks()
	if len(asks) != 2 {
		t.Fatalf("model asked %d times, want 2: once for the question, once with the cover's state", len(asks))
	}
	want := journal.Entry{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: garageState}
	if got := lastEntry(t, asks[1]); got != want {
		t.Errorf("follow-up ended on %+v, want the cover's state %+v", got, want)
	}
	if asks[1].Text != "is the garage door closed" {
		t.Errorf("follow-up Text = %q, want the question it still answers", asks[1].Text)
	}

	st := r.state(t, s.ConversationID())
	if want := []string{"Let me check.", "No, the garage door is open."}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want %q", st.Spoken, want)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindModelCompleted); n != 2 {
		t.Errorf("%d completions journalled, want 2: replay cannot regenerate either", n)
	}
}

// A reply that only speaks has nothing to be asked about.
//
// verifies SPEC §4.1
func TestSpeechAloneIsOneAsk(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Good morning, Teagan.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	s := r.open(t, "teagan")
	wait(t, heard(s, "good morning"))

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("model asked %d times, want 1", n)
	}
}

// Saying goodbye and ending the session is the end of the conversation, not
// a question for the model.
//
// verifies SPEC §4.5
func TestEndingTheSessionIsNotAskedAbout(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Goodnight.", Last: true}},
		{act: session.ToolCall{ID: "call_e1", Tool: "end_session", Args: "{}"}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	s := r.open(t, "alice")
	wait(t, heard(s, "that's all, goodnight"))
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("end_session did not close the session")
	}

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("model asked %d times, want 1", n)
	}
}

// "Turn off the lights and goodnight": the model acts and says goodbye in
// one breath. The session closes only once the farewell has played, so
// without a rule it would ask again while "Goodnight." is still on the
// speaker, and the answer could speak or act after the goodbye.
//
// verifies SPEC §4.5
func TestAGoodbyeBesideAToolCallIsNotAskedAbout(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Lights off. Goodnight.", Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","area_id":"bedroom"}`}},
		{act: session.ToolCall{ID: "call_e1", Tool: "end_session", Args: "{}"}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{"ha_call_service": reads(`{"changed":["light.bedroom"]}`)})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s2", Text: "The bedroom lights are off.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}
	// The farewell is still playing when the call returns, which is the
	// ordinary case: playback outlasts a local service call.
	r.speaker.hold = true

	s := r.open(t, "teagan")
	errc := heard(s, "turn off the lights and goodnight")
	r.speaker.wrote(t)
	r.awaitCall(t, s.ConversationID(), "call_c1")
	close(r.speaker.release)
	wait(t, errc)

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("model asked %d times after it said goodnight, want 1", n)
	}
}

// Turning off the kitchen lights, then "and the porch light too": the
// second ask has to carry the first exchange, or "too" means nothing.
//
// verifies SPEC §4.4
func TestTheNextUtteranceCarriesTheConversationSoFar(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{"ha_call_service": reads(`{"changed":["light.kitchen"]}`)})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Kitchen lights are off.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}

	s := r.open(t, "teagan")
	wait(t, heard(s, "turn off the kitchen lights"))
	r.engine.steps = []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	wait(t, heard(s, "and the porch light too"))

	asks := r.engine.asks()
	got := asks[len(asks)-1].Dialogue
	want := []journal.Entry{
		{Kind: journal.EntryHeard, Text: "turn off the kitchen lights", Speaker: "teagan"},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_call_service", Outcome: "ok", Result: `{"changed":["light.kitchen"]}`},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Kitchen lights are off."},
		{Kind: journal.EntryHeard, Text: "and the porch light too", Speaker: "teagan"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("second utterance's dialogue =\n%+v\nwant\n%+v", got, want)
	}
}

// A model that keeps calling tools is stopped by the cap, not by the
// household's patience.
//
// verifies SPEC §7
func TestTheRoundCapStopsAModelThatNeverStopsCallingTools(t *testing.T) {
	again := func(n int) []step {
		return []step{
			{act: session.ToolCall{ID: fmt.Sprintf("call_c%d", n), Tool: "ha_find_entities", Args: `{"domain":"light","name":"porch"}`}},
			{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
		}
	}
	r := newRig(t, again(1), map[string]session.Tool{"ha_find_entities": reads(`{"entities":[]}`)})
	for n := 2; n <= 10; n++ {
		r.engine.then = append(r.engine.then, again(n))
	}

	s := r.open(t, "teagan")
	wait(t, heard(s, "turn on the porch light"))

	if n := len(r.engine.asks()); n != session.DefaultRounds {
		t.Errorf("model asked %d times, want the cap of %d", n, session.DefaultRounds)
	}
	if n := len(r.state(t, s.ConversationID()).Calls); n != session.DefaultRounds {
		t.Errorf("%d calls made, want one per ask", n)
	}
}

// A failure is a result too, so the model is asked about it and can say
// what went wrong in its own words (SPEC §7).
//
// verifies SPEC §7
func TestAFailedToolIsAskedAboutSoTheModelCanSaySo(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{
		"ha_call_service": session.ToolFunc(func(context.Context, string) (string, error) {
			return "", errors.New("home assistant: 502 Bad Gateway")
		}),
	})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "I couldn't reach Home Assistant.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}

	s := r.open(t, "teagan")
	wait(t, heard(s, "turn off the kitchen lights"))

	asks := r.engine.asks()
	if len(asks) != 2 {
		t.Fatalf("model asked %d times, want 2", len(asks))
	}
	if got := lastEntry(t, asks[1]); got.Outcome != "error" || got.Result != `{"error":"home assistant: 502 Bad Gateway"}` {
		t.Errorf("follow-up ended on %+v, want the failure", got)
	}
	if want := []string{"I couldn't reach Home Assistant."}; !reflect.DeepEqual(r.state(t, s.ConversationID()).Spoken, want) {
		t.Errorf("spoken = %q, want %q", r.state(t, s.ConversationID()).Spoken, want)
	}
}

// Alice cuts the model off while it is still looking something up. The
// turn is over: asking the model again would answer a question she has
// already moved past.
//
// verifies SPEC §4.3
func TestABargeInEndsTheTurnWithoutAskingAgain(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "I found three albums by that artist", Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "media_search", Args: `{"query":"led zeppelin"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	gate := newGateTool(`{"hits":3}`)
	r := newRig(t, steps, map[string]session.Tool{"media_search": gate})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{CallID: "call_s2", Text: "Playing Led Zeppelin IV.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "play something by led zeppelin")
	gate.enter(t)
	r.speaker.wrote(t)
	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	close(gate.release)
	wait(t, errc)

	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("model asked %d times after a barge-in, want 1", n)
	}
	for _, said := range r.state(t, s.ConversationID()).Spoken {
		if said == "Playing Led Zeppelin IV." {
			t.Error("the follow-up spoke after the person cut the turn off")
		}
	}
}

// What the person heard of a cut answer is what the model is told it said,
// so it can pick up from there (SPEC §4.4).
//
// verifies SPEC §4.4
func TestTheNextAskIsToldWhereTheAnswerWasCut(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "I found three albums by that artist", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "play something by led zeppelin")
	r.speaker.wrote(t)
	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	wait(t, errc)
	r.speaker.hold = false
	r.engine.steps = []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	wait(t, heard(s, "just play the fourth one"))

	asks := r.engine.asks()
	got := asks[len(asks)-1].Dialogue[1]
	want := journal.Entry{Kind: journal.EntrySaid, CallID: "call_s1", Text: "I found three", Cut: true}
	if got != want {
		t.Errorf("cut answer told as %+v, want %+v", got, want)
	}
}

// Inline speech has no id of its own. Each ask's is a new utterance, so a
// follow-up that also speaks inline does not reuse the first ask's call.
//
// verifies SPEC §4.1
func TestEachAsksInlineSpeechIsItsOwnCall(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{Text: "Checking the thermostat.", Last: true}},
		{act: session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"climate.hallway"}`}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{"ha_get_state": reads(`{"entity_id":"climate.hallway","state":"heat","attributes":{"temperature":68}}`)})
	r.engine.then = [][]step{{
		{act: session.SpeechDelta{Text: "It's set to sixty eight.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}}

	s := r.open(t, "teagan")
	wait(t, heard(s, "what's the thermostat set to"))

	var speaks []journal.Call
	for _, c := range r.state(t, s.ConversationID()).Calls {
		if c.Tool == "speak" {
			speaks = append(speaks, c)
		}
	}
	if len(speaks) != 2 || speaks[0].ID == speaks[1].ID {
		t.Fatalf("speak calls = %+v, want two with their own ids", speaks)
	}
	// An utterance that arrived whole records its words with its call, so an
	// ask made while it is still playing knows what it is saying.
	if want := `{"mode":"queue","streamed":true,"text":"Checking the thermostat."}`; speaks[0].Args != want {
		t.Errorf("first speak args = %s, want %s", speaks[0].Args, want)
	}
}
