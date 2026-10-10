//go:build models

package ollama_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

var teagansCoffee = []journal.Memory{
	{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee."},
	{ID: "m_77d01b2e", Person: "alice", Fact: "The guest wifi password is on the fridge.", Shareable: true},
}

func called(acts []session.Action, tool string) *session.ToolCall {
	for _, a := range acts {
		if v, ok := a.(session.ToolCall); ok && v.Tool == tool {
			return &v
		}
	}
	return nil
}

// Asked to remember, a real model calls remember with the fact, and does not
// name whose it is: the session knows who is speaking.
//
// verifies SPEC §5
func TestARealModelRemembersWhenAsked(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "Remember that I take oat milk in my coffee."})
	c := called(acts, "remember")
	if c == nil {
		t.Fatalf("%s did not remember: %v", *model, describe(acts))
	}
	var args struct{ Fact string }
	if err := json.Unmarshal([]byte(c.Args), &args); err != nil || !strings.Contains(strings.ToLower(args.Fact), "oat milk") {
		t.Errorf("%s remembered %s, want the oat milk", *model, c.Args)
	}
}

// Told what it remembers, a real model answers from it.
//
// verifies SPEC §5
func TestARealModelAnswersFromWhatItRemembers(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "How do I take my coffee?", Memories: teagansCoffee})
	said := spoken(acts)
	if !strings.Contains(strings.ToLower(said), "oat") {
		t.Errorf("%s said %q, want the oat milk it remembers: %v", *model, said, describe(acts))
	}
}

// Asked to forget, a real model names the memory by the id it was shown.
//
// verifies SPEC §5
func TestARealModelForgetsByTheIDItWasShown(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "Forget what you know about how I take my coffee.", Memories: teagansCoffee})
	c := called(acts, "forget")
	if c == nil {
		t.Fatalf("%s did not forget: %v", *model, describe(acts))
	}
	var args struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal([]byte(c.Args), &args); err != nil || args.MemoryID != "m_3f9c2a10" {
		t.Errorf("%s forgot %s, want m_3f9c2a10", *model, c.Args)
	}
}

// spoken is everything a turn said aloud.
func spoken(acts []session.Action) string {
	said := ""
	for _, a := range acts {
		if v, ok := a.(session.SpeechDelta); ok {
			said += v.Text
		}
	}
	return said
}

// Thursday evening in the garage: Teagan asked, the door was open, and Alan
// said to close it.
var thursdayInTheGarage = []journal.Entry{
	{Kind: journal.EntryHeard, Text: "is the garage door closed", Speaker: "teagan"},
	{Kind: journal.EntryCall, CallID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
	{Kind: journal.EntryResult, CallID: "call_c1", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
	{Kind: journal.EntrySaid, CallID: "call_s1", Text: "The garage door is open. Want me to close it?"},
	{Kind: journal.EntryHeard, Text: "yes close it", Speaker: "alan"},
	{Kind: journal.EntryCall, CallID: "call_c2", Tool: "ha_call_service", Args: `{"domain":"cover","service":"close_cover","entity_id":"cover.garage_door"}`},
	{Kind: journal.EntryResult, CallID: "call_c2", Tool: "ha_call_service", Outcome: "ok", Result: `{}`},
	{Kind: journal.EntrySaid, CallID: "call_s2", Text: "Closing the garage door."},
}

// A real model summarizes a finished conversation in a sentence or two that
// says what was asked about and names who was there.
//
// verifies SPEC §5
func TestARealModelSummarizesWhoAskedWhat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), turnBudget)
	defer cancel()
	got, err := engine(t).Summarize(ctx, thursdayInTheGarage, []string{"teagan", "alan"})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	lower := strings.ToLower(got)
	if !strings.Contains(lower, "garage") || !strings.Contains(lower, "teagan") {
		t.Errorf("%s summarized %q, want the garage door and who asked", *model, got)
	}
	if n := strings.Count(got, ". ") + 1; n > 3 || len(got) > 400 || strings.Contains(got, "\n- ") {
		t.Errorf("%s wrote %d sentences, %d bytes: %q", *model, n, len(got), got)
	}
}

// Told the time and what Teagan asked last night, a real model answers
// "what did I ask you yesterday" from it.
//
// verifies SPEC §5
func TestARealModelAnswersWhatWasAskedYesterday(t *testing.T) {
	acts := ask(t, engine(t), session.Input{
		ConversationID: "models-test", Speaker: "teagan", Text: "What did I ask you yesterday?",
		Now: time.Date(2026, 10, 9, 8, 53, 0, 0, time.Local),
		Summaries: []journal.Summary{{
			ConversationID: "conv-garage-0812", At: time.Date(2026, 10, 8, 18, 4, 0, 0, time.Local),
			Text: "teagan asked whether the garage door was closed; it was open, and the assistant closed it when alan said to.",
		}},
	})
	if said := strings.ToLower(spoken(acts)); !strings.Contains(said, "garage") {
		t.Errorf("%s said %q, want last night's garage door: %v", *model, said, describe(acts))
	}
}

// Told the time, a real model knows what day it is.
//
// verifies SPEC §5
func TestARealModelKnowsWhatDayItIs(t *testing.T) {
	acts := ask(t, engine(t), session.Input{
		ConversationID: "models-test", Speaker: "alice", Text: "What day is it today?",
		Now: time.Date(2026, 10, 9, 8, 53, 0, 0, time.Local),
	})
	if said := strings.ToLower(spoken(acts)); !strings.Contains(said, "friday") {
		t.Errorf("%s said %q, want Friday: %v", *model, said, describe(acts))
	}
}
