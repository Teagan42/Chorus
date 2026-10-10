package main

import (
	"context"
	"strconv"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// The garage door's state comes back while the kitchen is reading Alan the
// forecast, and the model interjects. The kitchen stops where Alan's ear
// is, says the garage door, and picks the forecast up from there: the pause
// is cut at the stop's answer, and the rest plays as the same call.
//
// verifies SPEC §4.2, §4.4
func TestAnInterjectionPausesTheKitchenAndTheForecastResumes(t *testing.T) {
	const (
		forecast = "Cloudy tomorrow morning, then rain from three."
		garage   = "Sorry to cut in, the garage door is open."
	)
	looked := make(chan struct{})
	eng := &scriptEngine{
		acts: []session.Action{
			session.SpeechDelta{CallID: "call_w1", Text: forecast, Last: true},
			session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
			session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"},
		},
		answer: func(journal.Entry) []session.Action {
			return []session.Action{
				session.SpeechDelta{CallID: "call_g1", Text: garage, Mode: session.ModeInterject, Last: true},
				session.TurnEnd{FinishReason: "stop", Completion: "{}"},
			}
		},
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = map[string]session.Tool{"ha_get_state": session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
			select {
			case <-looked:
				return `{"entity_id":"cover.garage_door","state":"open"}`, nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		})}
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather tomorrow and is the garage shut", alan))

	dev.AwaitTTS(t, 2*len(forecast))
	heard := dev.Play(t, 10)
	// The kitchen gets a little further while the stop is in flight.
	dev.PlayDuringStop(6)
	close(looked)

	dev.AwaitStop(t, 1)
	pause := r.store.awaitKind(t, journal.KindSpeechTruncated, 1)
	want := map[string]string{
		"spoken_text": "Cloudy tomorrow morning, ", "unspoken_text": "then rain from three.",
		"frames_played": strconv.FormatUint(heard+6, 10), "call_id": "call_w1", "reason": "interjected",
	}
	for k, v := range want {
		if pause.Fields[k] != v {
			t.Errorf("pause %s = %q, want %q", k, pause.Fields[k], v)
		}
	}

	dev.AwaitTTS(t, 2*len(forecast)+2*len(garage))
	dev.PlayAll(t)
	said := r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	if said.Fields["call_id"] != "call_g1" || said.Fields["text"] != garage {
		t.Errorf("first heard whole = %v, want the garage door", said.Fields)
	}
	dev.AwaitTTS(t, 2*len(forecast)+2*len(garage)+2*len("then rain from three."))
	dev.PlayAll(t)
	resumed := r.spoken(t, "kitchen", 2)
	if resumed.Fields["call_id"] != "call_w1" || resumed.Fields["text"] != "then rain from three." {
		t.Errorf("resumed = %v, want the rest of the forecast as call_w1", resumed.Fields)
	}
	if n := dev.Stops(); n != 1 {
		t.Errorf("%d stops, want the one that paused the forecast", n)
	}

	st, err := journal.Replay(t.Context(), r.store, pause.ConversationID, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, e := range st.Dialogue {
		if e.CallID == "call_w1" && (e.Text != forecast || e.Cut || e.Pending) {
			t.Errorf("forecast in the dialogue = %+v, want all of it, heard", e)
		}
	}
}
