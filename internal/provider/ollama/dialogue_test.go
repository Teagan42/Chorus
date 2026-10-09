package ollama

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// sent runs one ask and returns the messages the request carried.
func sent(t *testing.T, in session.Input) []message {
	t.Helper()
	rt := &roundTrip{body: fixture(t, "reply.ndjson")}
	e := engineOn(t, rt, Config{})
	ch, err := e.Turn(context.Background(), in)
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)
	var got struct {
		Messages []message `json:"messages"`
	}
	if err := json.Unmarshal(rt.reqBody, &got); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	return got.Messages
}

func call(id, name, args string) toolCall {
	var tc toolCall
	tc.ID = id
	tc.Function.Name = name
	tc.Function.Arguments = json.RawMessage(args)
	return tc
}

// The follow-up ask after Teagan's garage question: the model said it was
// checking and read the cover, and is now asked with the cover's state. It
// has to see its own calls, grouped as it made them, and the result named by
// the tool that returned it, which is how this endpoint matches the two.
//
// verifies SPEC §4.1, §4.4
func TestAFollowUpCarriesTheCallsAndWhatTheyReturned(t *testing.T) {
	msgs := sent(t, session.Input{
		Speaker: "teagan",
		Text:    "is the garage door closed",
		Dialogue: []journal.Entry{
			{Kind: journal.EntryHeard, Text: "is the garage door closed"},
			{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Let me check.", Pending: true},
			{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
			{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
		},
	})

	want := []message{
		{Role: "user", Content: "is the garage door closed"},
		{Role: "assistant", ToolCalls: []toolCall{call("call_s1", "speak", `{"text":"Let me check."}`)}},
		{Role: "tool", ToolName: "speak", Content: `{"playing":true}`},
		{Role: "assistant", ToolCalls: []toolCall{call("call_c1", "ha_get_state", `{"entity_id":"cover.garage_door"}`)}},
		{Role: "tool", ToolName: "ha_get_state", Content: `{"entity_id":"cover.garage_door","state":"open"}`},
	}
	if got := msgs[1:]; !reflect.DeepEqual(got, want) {
		t.Errorf("messages =\n%+v\nwant\n%+v", got, want)
	}
	if msgs[0].Role != "system" {
		t.Errorf("first message is %q, want the system prompt", msgs[0].Role)
	}
}

// Calls the model made back to back go back in one assistant message, the
// shape it produced them in.
//
// verifies SPEC §4.1
func TestCallsMadeTogetherGoBackTogether(t *testing.T) {
	msgs := sent(t, session.Input{Dialogue: []journal.Entry{
		{Kind: journal.EntryHeard, Text: "turn off the kitchen and porch lights"},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`},
		{Kind: journal.EntryCall, CallID: "call_c2", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.porch"}`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_call_service", Outcome: "ok", Result: `{"changed":["light.kitchen"]}`},
		{Kind: journal.EntryResult, CallID: "call_c2", Tool: "ha_call_service", Outcome: "ok", Result: `{"changed":["light.porch"]}`},
	}})
	if n := len(msgs[2].ToolCalls); msgs[2].Role != "assistant" || n != 2 {
		t.Errorf("assistant message = %+v, want both calls in it", msgs[2])
	}
	if len(msgs) != 5 {
		t.Errorf("%d messages, want system, user, one assistant and two results", len(msgs))
	}
}

// The model is told it was cut off and what of its answer was heard, never
// the words that were not (SPEC §4.4).
//
// verifies SPEC §4.4
func TestACutAnswerGoesBackAsTheHeardWordsAndTheCut(t *testing.T) {
	msgs := sent(t, session.Input{Dialogue: []journal.Entry{
		{Kind: journal.EntryHeard, Text: "play something by led zeppelin"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "I found three", Cut: true},
		{Kind: journal.EntryHeard, Text: "just play the fourth one"},
	}})
	said := msgs[2].ToolCalls[0]
	if string(said.Function.Arguments) != `{"text":"I found three"}` {
		t.Errorf("cut speech sent as %s, want only the heard words", said.Function.Arguments)
	}
	if got := msgs[3].Content; got != `{"interrupted":true,"note":"the person cut you off and heard only the text in this call"}` {
		t.Errorf("cut speech result = %s", got)
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" || last.Content != "just play the fourth one" {
		t.Errorf("last message = %+v, want the new utterance", last)
	}
}

// A failure says how it ended before what it said, so a timeout is not read
// as an answer (SPEC §7).
//
// verifies SPEC §7
func TestAFailedCallSaysHowItEnded(t *testing.T) {
	for _, tc := range []struct {
		entry journal.Entry
		want  string
	}{
		{journal.Entry{Outcome: "timed_out", Result: `{"error":"timed_out"}`}, `{"outcome":"timed_out","result":{"error":"timed_out"}}`},
		{journal.Entry{Outcome: "cancelled"}, `{"outcome":"cancelled"}`},
		{journal.Entry{Outcome: "detached", Result: `{"hits":3}`}, `{"outcome":"detached","result":{"hits":3}}`},
		{journal.Entry{Outcome: "ok"}, `{}`},
	} {
		if got := result(tc.entry); got != tc.want {
			t.Errorf("result(%s) = %s, want %s", tc.entry.Outcome, got, tc.want)
		}
	}
}

// A model that wrote broken arguments gets its call back as an empty object:
// the result already says it failed, and resending the bytes would fail the
// whole ask at the endpoint.
//
// verifies SPEC §7
func TestBrokenArgumentsDoNotBreakTheNextAsk(t *testing.T) {
	msgs := sent(t, session.Input{Dialogue: []journal.Entry{
		{Kind: journal.EntryHeard, Text: "set a timer for twelve minutes"},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "set_timer", Args: `{"minutes":12`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "set_timer", Outcome: "error", Result: `{"error":"unknown_tool"}`},
	}})
	if got := string(msgs[2].ToolCalls[0].Function.Arguments); got != "{}" {
		t.Errorf("broken arguments sent as %s, want {}", got)
	}
}

// An ask with no dialogue, as Replay's re-runs make, still carries its
// utterance.
//
// verifies SPEC §12
func TestAnAskWithoutDialogueSendsTheUtterance(t *testing.T) {
	msgs := sent(t, session.Input{Text: "what's the weather on saturday"})
	if len(msgs) != 2 || msgs[1].Role != "user" || msgs[1].Content != "what's the weather on saturday" {
		t.Errorf("messages = %+v, want the system prompt and the utterance", msgs)
	}
}

// Streamed speech whose words are not known until it finishes playing is
// left out rather than told as an empty speak call.
//
// verifies SPEC §4.4
func TestSpeechWithNoWordsYetIsLeftOut(t *testing.T) {
	msgs := sent(t, session.Input{Dialogue: []journal.Entry{
		{Kind: journal.EntryHeard, Text: "what's the thermostat set to"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Pending: true},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"climate.hallway"}`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: `{"state":"heat"}`},
	}})
	for _, m := range msgs {
		for _, c := range m.ToolCalls {
			if c.Function.Name == "speak" {
				t.Errorf("an empty speak call was sent: %+v", m)
			}
		}
	}
	if len(msgs) != 4 {
		t.Errorf("%d messages, want system, user, the call and its result", len(msgs))
	}
}
