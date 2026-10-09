//go:build models

package ollama_test

import (
	"encoding/json"
	"strings"
	"testing"

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
	said := ""
	for _, a := range acts {
		if v, ok := a.(session.SpeechDelta); ok {
			said += v.Text
		}
	}
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
