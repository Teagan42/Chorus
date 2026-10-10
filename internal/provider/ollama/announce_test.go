package ollama

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// wineInTheKitchen is the kitchen's conversation after Teagan, in the
// office, asked it whether anyone wanted wine, and Alan answered.
var wineInTheKitchen = []journal.Entry{
	{Kind: journal.EntrySaid, CallID: "an_4c1f", Text: "Dinner is ready. Does anyone want wine?", Announces: &journal.Announcement{
		CallID: "an_4c1f", Text: "Dinner is ready. Does anyone want wine?", Source: "request",
		RequestedBy: "teagan", FromSatellite: "office", FromConversation: "conv-1838-office", StartConversation: true,
	}},
	{Kind: journal.EntryHeard, Text: "yes a glass of red please", Speaker: "alan"},
}

// The model asked in the kitchen is shown the question as a speak call it
// made, and its result says whose question it was and from which room, so
// "yes, a glass of red" can be taken back to the office (SPEC §4).
//
// verifies SPEC §4
func TestAnAnnouncementTellsTheModelWhoseQuestionItAsked(t *testing.T) {
	msgs := sent(t, session.Input{Speaker: "alan", Text: "yes a glass of red please", Dialogue: wineInTheKitchen})
	want := []message{
		{Role: "assistant", ToolCalls: []toolCall{call("an_4c1f", "speak", `{"text":"Dinner is ready. Does anyone want wine?"}`)}},
		{Role: "tool", ToolName: "speak", Content: `{"heard":true,"announcement":{"source":"request","requested_by":"teagan","from":"office","answer_expected":true}}`},
		{Role: "user", Content: "yes a glass of red please"},
	}
	if !reflect.DeepEqual(msgs[1:], want) {
		t.Errorf("messages = %+v\nwant %+v", msgs[1:], want)
	}
}

// A timer going off in the middle of a conversation is said there; asked
// next, the model can see it was the oven, not something it chose to say.
//
// verifies SPEC §4
func TestATimerGoingOffIsMarkedInTheDialogue(t *testing.T) {
	got := heard(journal.Entry{Kind: journal.EntrySaid, Text: "The oven timer is done.", Announces: &journal.Announcement{
		Source: "timer", TimerID: "t_0a7e11c3", RequestedBy: "alan", FromSatellite: "kitchen",
	}})
	if want := `{"heard":true,"announcement":{"source":"timer","timer_id":"t_0a7e11c3","requested_by":"alan","from":"kitchen"}}`; got != want {
		t.Errorf("result = %s, want %s", got, want)
	}
	if got := heard(journal.Entry{Kind: journal.EntrySaid, Text: "The oven timer", Cut: true, Announces: &journal.Announcement{Source: "timer"}}); !strings.HasPrefix(got, `{"interrupted":true,`) || !strings.HasSuffix(got, `"announcement":{"source":"timer"}}`) {
		t.Errorf("a cut announcement = %s", got)
	}
}

// The summary says the question was asked unasked, and for whom.
//
// verifies SPEC §5
func TestASummaryKnowsAnAnnouncementWasNotTheAssistantsIdea(t *testing.T) {
	rt := &roundTrip{body: fixture(t, "summary.json")}
	if _, err := engineOn(t, rt, Config{}).Summarize(context.Background(), wineInTheKitchen, []string{"alan"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rt.reqBody), `the assistant said \"Dinner is ready. Does anyone want wine?\", unasked, as teagan asked from the office`) {
		t.Errorf("transcript:\n%s", rt.reqBody)
	}
	if got := because(&journal.Announcement{Source: "timer"}); got != "as a timer went off" {
		t.Errorf("because a timer = %q", got)
	}
}
