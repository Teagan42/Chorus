package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

const (
	ovenDone   = "The oven timer is done."
	ovenSet    = "Twelve minutes on the oven."
	wineAsk    = "Dinner is ready. Does anyone want wine?"
	setTheOven = `{"seconds":720,"label":"oven","announcement":"` + ovenDone + `"}`
)

// timerModel sets the oven when asked and says so once the timer is running.
func timerModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "set a timer for twelve minutes for the oven":
			return []session.Action{session.ToolCall{ID: "call_t1", Tool: "timer_start", Args: setTheOven}, done}
		case last.Kind == journal.EntryResult && last.Tool == "timer_start":
			return []session.Action{
				session.SpeechDelta{CallID: "call_s1", Text: ovenSet, Last: true},
				session.ToolCall{ID: "call_e1", Tool: "end_session", Args: "{}"},
				done,
			}
		}
		return []session.Action{done}
	}}
}

// setOven is Alan asking the kitchen for the oven timer and hearing it set.
// It returns how much speech the kitchen has been sent.
func setOven(t *testing.T, r *rig, dev *bridgetest.Device) int {
	t.Helper()
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("set a timer for twelve minutes for the oven", alan))
	r.store.awaitKind(t, journal.KindTimerStarted, 1)
	dev.AwaitTTS(t, 2*len(ovenSet))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSessionClosed, 1)
	return 2 * len(ovenSet)
}

// Alan sets the oven timer in the kitchen and walks away; the conversation
// ends. Twelve minutes later, with nobody talking to it, the kitchen says
// the timer is done in a session of its own, and the house log says it was
// heard there (SPEC §4, ADR-0045).
//
// verifies SPEC §4, §8
func TestTheOvenTimerGoesOffInTheKitchen(t *testing.T) {
	clk := newClock()
	r := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Clock, d.Timers = timerModel(), clk, clk
	})
	kitchen := r.join(t, kitchenIP)
	office := r.join(t, officeIP)
	sent := setOven(t, r, kitchen)

	set := r.store.ofKind(journal.KindTimerStarted)[0]
	if set.ConversationID != journal.HouseTimers || set.Fields["satellite"] != "kitchen" || set.Fields["person"] != "alan" {
		t.Errorf("timer_started = %v in %s", set.Fields, set.ConversationID)
	}
	if want := epoch.Add(12 * time.Minute).Format(time.RFC3339Nano); set.Fields["fires_at"] != want {
		t.Errorf("fires_at = %s, want %s", set.Fields["fires_at"], want)
	}

	clk.advance(12 * time.Minute)
	kitchen.AwaitTTS(t, sent+2*len(ovenDone))
	kitchen.PlayAll(t)
	finished := r.store.awaitKind(t, journal.KindTimerFinished, 1)
	if finished.Fields["outcome"] != "announced" {
		t.Fatalf("timer_finished = %v", finished.Fields)
	}
	conv := finished.Fields["conversation_id"]
	events, err := r.store.Events(context.Background(), conv)
	if err != nil || len(events) == 0 {
		t.Fatalf("the announcement's conversation %q: %v", conv, err)
	}
	if events[0].Kind != journal.KindSessionOpened || events[0].Fields["announced"] != "true" || events[0].Fields["satellite"] != "kitchen" {
		t.Errorf("opened with %s %v", events[0].Kind, events[0].Fields)
	}
	await(t, "the announcement to close", func() bool {
		events, _ = r.store.Events(context.Background(), conv)
		return events[len(events)-1].Kind == journal.KindSessionClosed
	})
	var spoken string
	for _, e := range events {
		if e.Kind == journal.KindSpeechSpoken {
			spoken = e.Fields["text"]
		}
	}
	if spoken != ovenDone || events[len(events)-1].Fields["reason"] != "announced" {
		t.Errorf("the kitchen said %q and closed as %q", spoken, events[len(events)-1].Fields["reason"])
	}
	if got := len(office.TTS()); got != 0 {
		t.Errorf("the office heard %d bytes of the kitchen's timer", got)
	}
}

// chorusd restarts while the oven timer is running, and comes back up before
// the kitchen satellite has dialled in again. The new daemon reads the timer
// from the house log, and says it once the kitchen is back, a few seconds
// late rather than never.
//
// verifies SPEC §7, §8
func TestATimerSurvivesARestartAndWaitsForItsSatellite(t *testing.T) {
	clk := newClock()
	first := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Clock, d.Timers = timerModel(), clk, clk
	})
	kitchen := first.join(t, kitchenIP)
	setOven(t, first, kitchen)
	first.cancel()
	if err := first.exit(t); err != nil {
		t.Fatalf("first daemon: %v", err)
	}

	clk.advance(12*time.Minute + 30*time.Second)
	second := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Clock, d.Timers, d.Store = timerModel(), clk, clk, first.store
	})
	// Due, with nothing to say it on: the scheduler tries again shortly.
	clk.awaitWait(t, 5*time.Second)
	back := second.join(t, kitchenIP)
	clk.advance(5 * time.Second)

	back.AwaitTTS(t, 2*len(ovenDone))
	back.PlayAll(t)
	finished := first.store.awaitKind(t, journal.KindTimerFinished, 1)
	if finished.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v, want announced once the kitchen was back", finished.Fields)
	}
}

// Alan, in the office, asks the kitchen whether anyone wants wine and to
// listen for the answer. The kitchen says it, nobody wakes it, and the guest
// at the counter answers. The kitchen's model is asked with the question it
// was given, and whose question it was.
//
// verifies SPEC §4, §4.5
func TestAQuestionForTheKitchenIsAnsweredThereWithNoWakeWord(t *testing.T) {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	ask := `{"text":"` + wineAsk + `","room":"kitchen","start_conversation":true}`
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		if last.Kind == journal.EntryHeard && last.Text == "ask the kitchen if anyone wants wine with dinner" {
			return []session.Action{session.ToolCall{ID: "call_a1", Tool: "announce", Args: ask}, done}
		}
		return []session.Action{done}
	}}
	r := newRig(t, inventory(), func(d *deps) { d.engine = eng })
	office := r.join(t, officeIP)
	kitchen := r.join(t, kitchenIP)

	office.SendWake(t, "hey_eddie")
	r.utter(t, office, r.line("ask the kitchen if anyone wants wine with dinner", alan))
	result := r.store.awaitKind(t, journal.KindToolResult, 1)
	if result.Fields["outcome"] != "ok" || result.Fields["result_json"] != `{"announced_in":["kitchen"]}` {
		t.Errorf("announce = %v", result.Fields)
	}
	kitchen.AwaitTTS(t, 2*len(wineAsk))
	kitchen.PlayAll(t)
	made := r.store.awaitKind(t, journal.KindAnnouncementMade, 1)
	r.spoken(t, "kitchen", 1)

	r.utter(t, kitchen, r.line("yes please a glass of red", stranger))
	heard := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2)
	if heard.ConversationID != made.ConversationID {
		t.Fatalf("the answer was heard in %s, want the question's %s", heard.ConversationID, made.ConversationID)
	}
	var in session.Input
	await(t, "the kitchen's turn", func() bool {
		for _, asked := range eng.heard() {
			if asked.ConversationID == made.ConversationID {
				in = asked
				return true
			}
		}
		return false
	})
	if len(in.Dialogue) != 2 || in.Speaker != "" {
		t.Fatalf("the kitchen's model was asked %+v, want the question then a guest's answer", in)
	}
	a := in.Dialogue[0].Announces
	if a == nil || a.RequestedBy != "alan" || a.FromSatellite != "office" || !a.StartConversation {
		t.Errorf("the kitchen's model was not told whose question it asked: %+v", a)
	}
	b, _ := json.Marshal(made.Fields)
	if made.Fields["from_conversation"] != result.ConversationID {
		t.Errorf("announcement_made = %s, want it to name the office's conversation", b)
	}
}
