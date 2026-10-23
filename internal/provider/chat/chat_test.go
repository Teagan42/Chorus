package chat

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// Each result names both the tool and the call it answers, so an endpoint
// that pairs them by name and one that pairs them by id are told the same
// dialogue; calls made together stay together, and broken arguments go
// back as {} (ADR-0037).
//
// verifies SPEC §4.1, §4.4
func TestTheDialogueNamesEachResultsCallAndTool(t *testing.T) {
	got := Dialogue([]journal.Entry{
		{Kind: journal.EntryHeard, Text: "turn off the kitchen and porch lights"},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Turning them off.", Pending: true},
		{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`},
		{Kind: journal.EntryCall, CallID: "call_c2", Tool: "ha_call_service", Args: `{"domain":"light"`},
		{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_call_service", Outcome: "ok", Result: `{"changed":["light.kitchen"]}`},
		{Kind: journal.EntryResult, CallID: "call_c2", Tool: "ha_call_service", Outcome: "error", Result: `{"error":"invalid_arguments"}`},
	})
	want := []Step{
		{Role: "user", Content: "turn off the kitchen and porch lights"},
		{Role: "assistant", Calls: []Call{{ID: "call_s1", Name: "speak", Args: json.RawMessage(`{"text":"Turning them off."}`)}}},
		{Role: "tool", Tool: "speak", CallID: "call_s1", Content: `{"playing":true}`},
		{Role: "assistant", Calls: []Call{
			{ID: "call_c1", Name: "ha_call_service", Args: json.RawMessage(`{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`)},
			{ID: "call_c2", Name: "ha_call_service", Args: json.RawMessage(`{}`)},
		}},
		{Role: "tool", Tool: "ha_call_service", CallID: "call_c1", Content: `{"changed":["light.kitchen"]}`},
		{Role: "tool", Tool: "ha_call_service", CallID: "call_c2", Content: `{"outcome":"error","result":{"error":"invalid_arguments"}}`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dialogue =\n%+v\nwant\n%+v", got, want)
	}
}

// The tool schema's version is a hash of the declarations as the wire
// carries them, and an unedited schema reads back to the same version.
//
// verifies SPEC §8, §9.2
func TestTheToolSchemaReadsBackToItsOwnVersion(t *testing.T) {
	specs, err := ParseToolSchema(ToolSchema(registry.Offered()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ToolSchemaVersion(specs) != ToolSchemaVersion(registry.Offered()) {
		t.Error("the schema read back has another version")
	}
	if v := PromptVersion(DefaultPrompt); !strings.HasPrefix(v, "sha256:") || len(v) != len("sha256:")+12 {
		t.Errorf("prompt version = %q", v)
	}
}

// Content is an answer only from a turn that called nothing, kept its
// reasoning apart and ended on its own; the speak tool's name leading it
// is not said (ADR-0046).
//
// verifies SPEC §4.1
func TestContentIsAnAnswerInOneShapeOnly(t *testing.T) {
	for _, c := range []struct {
		content          string
		called, reasoned bool
		reason, want     string
	}{
		{content: "Today is Friday.", reasoned: true, reason: "stop", want: "Today is Friday."},
		{content: "speak\nThe garage door code is 4512.", reasoned: true, reason: "stop", want: "The garage door code is 4512."},
		{content: "<think>It is Friday.</think>\n\nToday is Friday.", reasoned: true, reason: "stop", want: "Today is Friday."},
		{content: "Today is Friday.", called: true, reasoned: true, reason: "tool_calls"},
		{content: "Today is Friday.", reasoned: true, reason: "length"},
		{content: "Okay, the user is asking about the day.", reason: "stop"},
	} {
		if got := Answer(c.content, c.called, c.reasoned, c.reason); got != c.want {
			t.Errorf("Answer(%q, called=%v, reasoned=%v, %s) = %q, want %q", c.content, c.called, c.reasoned, c.reason, got, c.want)
		}
	}
}

// A turn asked in its pause is kept only if the confirmed turn would ask the
// same, and the session compares the time to the minute (ADR-0095). That is
// only sound while the model is told it to the minute: a second here would
// make every kept answer one the confirmed turn would not have asked.
//
// verifies SPEC §5, §11
func TestTheTimeIsToldToTheMinute(t *testing.T) {
	loc := time.FixedZone("PDT", -7*3600)
	at := func(sec int) string {
		return System("You are the house.", session.Input{
			Speaker: "teagan", Room: "kitchen",
			Now: time.Date(2026, 10, 9, 1, 4, sec, 0, time.UTC),
		}, loc)
	}
	if at(1) != at(59) {
		t.Errorf("the pause and the end of one minute are told apart:\n%s\n%s", at(1), at(59))
	}
	next := System("You are the house.", session.Input{
		Speaker: "teagan", Room: "kitchen", Now: time.Date(2026, 10, 9, 1, 5, 0, 0, time.UTC),
	}, loc)
	if next == at(59) {
		t.Error("the next minute is told as the last")
	}
}
