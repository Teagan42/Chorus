//go:build models

package ollama_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// Asked for the oven timer, a real model starts one for twelve minutes and
// labels it for the oven.
//
// verifies SPEC §6
func TestARealModelSetsTheOvenTimer(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "alan", Text: "Set a timer for twelve minutes for the oven."})
	c := called(acts, "timer_start")
	if c == nil {
		t.Fatalf("%s set no timer: %v", *model, describe(acts))
	}
	var args struct {
		Seconds int
		Label   string
	}
	if err := json.Unmarshal([]byte(c.Args), &args); err != nil || args.Seconds != 720 || !strings.Contains(strings.ToLower(args.Label), "oven") {
		t.Errorf("%s started %s, want 720 seconds for the oven", *model, c.Args)
	}
}

// Told the oven timer's id when it was set, a real model cancels it by that
// id rather than inventing one.
//
// verifies SPEC §6
func TestARealModelCancelsTheTimerByTheIDItWasGiven(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "alan", Dialogue: []journal.Entry{
		{Kind: journal.EntryHeard, Text: "set a timer for twelve minutes for the oven", Speaker: "alan"},
		{Kind: journal.EntryCall, CallID: "call_t1", Tool: "timer_start", Args: `{"seconds":720,"label":"oven"}`},
		{Kind: journal.EntryResult, CallID: "call_t1", Tool: "timer_start", Outcome: "ok", Result: `{"timer_id":"t_0a7e11c3","label":"oven","satellite":"kitchen","seconds_left":720,"says":"The oven timer is done."}`},
		{Kind: journal.EntrySaid, CallID: "call_s1", Text: "Twelve minutes on the oven."},
		{Kind: journal.EntryHeard, Text: "actually cancel the oven timer", Speaker: "alan"},
	}})
	c := called(acts, "timer_cancel")
	if c == nil {
		t.Fatalf("%s cancelled nothing: %v", *model, describe(acts))
	}
	var args struct {
		TimerID string `json:"timer_id"`
	}
	if err := json.Unmarshal([]byte(c.Args), &args); err != nil || args.TimerID != "t_0a7e11c3" {
		t.Errorf("%s cancelled %s, want t_0a7e11c3", *model, c.Args)
	}
}

// Asked from the office to find out whether anyone in the kitchen wants
// wine, a real model announces it there and listens for the answer.
//
// verifies SPEC §4
func TestARealModelAsksTheKitchenAndListensForTheAnswer(t *testing.T) {
	acts := ask(t, engine(t), session.Input{ConversationID: "models-test", Speaker: "teagan", Text: "Ask the kitchen whether anyone wants wine with dinner, and listen for the answer."})
	c := called(acts, "announce")
	if c == nil {
		t.Fatalf("%s announced nothing: %v", *model, describe(acts))
	}
	var args struct {
		Text              string
		Room              string
		StartConversation bool `json:"start_conversation"`
	}
	if err := json.Unmarshal([]byte(c.Args), &args); err != nil || !strings.EqualFold(args.Room, "kitchen") || !args.StartConversation || !strings.Contains(strings.ToLower(args.Text), "wine") {
		t.Errorf("%s announced %s, want a question about wine for the kitchen, listening", *model, c.Args)
	}
}
