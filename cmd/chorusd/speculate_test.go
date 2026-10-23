package main

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

// kitchenLights is the household's model for an evening in the kitchen: it
// says the lights are on, turns them off when asked, and says nothing more
// once Home Assistant has answered.
func kitchenLights(in session.Input) []session.Action {
	end := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	if _, ok := lastResult(in); ok {
		return []session.Action{end}
	}
	switch in.Text {
	case "are the kitchen lights on":
		return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "They are.", Last: true}, end}
	case "turn them off":
		return []session.Action{
			session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`},
			session.SpeechDelta{CallID: "call_s2", Text: "Kitchen lights off.", Last: true},
			session.TurnEnd{FinishReason: "tool_calls", Completion: `{"tool_calls":["ha_call_service"]}`},
		}
	case "turn them off and the hallway too":
		return []session.Action{
			session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`},
			session.ToolCall{ID: "call_c3", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_off","entity_id":"light.hallway"}`},
			session.TurnEnd{FinishReason: "tool_calls", Completion: `{"tool_calls":["ha_call_service"]}`},
		}
	}
	return []session.Action{end}
}

// speculatingDaemon runs chorusd with CHORUS_SPECULATE on, over Home
// Assistant in-process, with Alan one answered turn into the kitchen.
func speculatingDaemon(t *testing.T) (*rig, *bridgetest.Device, *homeAssistant) {
	t.Helper()
	ha := &homeAssistant{answers: map[string]string{
		"POST /api/services/light/turn_off": `[{"entity_id":"light.kitchen","state":"off"}]`,
	}}
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = &scriptEngine{decide: kitchenLights}
		d.tools = hass.Tools(client)
		d.speculate = true
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("are the kitchen lights on", alan))
	dev.AwaitTTS(t, 2*len("They are."))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	r.store.awaitKind(t, journal.KindModelCompleted, 1)
	await(t, "the first turn to settle", func() bool {
		s := r.dm.linked("kitchen").Session()
		return s != nil && slices.Equal(s.Children(), []string{"listening"})
	})
	return r, dev, ha
}

// speakFor sends a line's voice, then quiet up to the endpointer's pause.
func speakFor(t *testing.T, dev *bridgetest.Device, amplitude int16, chunks int) {
	t.Helper()
	for range chunks {
		dev.SendMic(t, bridge.ChannelAEC, voice(amplitude, chunkBytes))
	}
	for sent := 0; sent < listen.DefaultPause; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
}

func lightsPosted(ha *homeAssistant) []string {
	var out []string
	for i, p := range ha.paths() {
		if strings.HasPrefix(p, "POST ") {
			out = append(out, p+" "+ha.sent()[i])
		}
	}
	return out
}

// Alan says "turn them off" and stops. chorusd asks the model in the 200 ms
// pause, before the 800 ms of quiet that ends the utterance, and nothing
// reaches Home Assistant or the speaker until it does. Then the lights go
// off on the answer already given, and the log reads as a turn asked late.
//
// verifies SPEC §4.1, §4.5, §8, §11
func TestChorusdAnswersATurnInItsPause(t *testing.T) {
	r, dev, ha := speculatingDaemon(t)
	eng := r.dm.engine.(*scriptEngine)

	speakFor(t, dev, r.line("turn them off", alan), 8)
	await(t, "the pause to be answered", func() bool { return len(eng.heard()) == 2 })
	if in := eng.heard()[1]; in.Text != "turn them off" || in.Speaker != "alan" || in.Room != "kitchen" {
		t.Errorf("the pause asked %q for %q in %q", in.Text, in.Speaker, in.Room)
	}
	if got := lightsPosted(ha); len(got) != 0 {
		t.Fatalf("Home Assistant was called before the turn ended: %v", got)
	}
	if n := len(r.store.ofKind(journal.KindUtteranceTranscribed)); n != 1 {
		t.Fatalf("%d utterances heard in the pause, want only the first", n)
	}

	for sent := listen.DefaultPause; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2)
	dev.AwaitTTS(t, 2*len("They are.")+2*len("Kitchen lights off."))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 2)
	r.store.awaitKind(t, journal.KindModelCompleted, 3)

	if got := lightsPosted(ha); len(got) != 1 || !strings.Contains(got[0], "light.kitchen") {
		t.Errorf("Home Assistant got %v, want the kitchen lights off once", got)
	}
	var early []string
	for _, e := range r.store.ofKind(journal.KindModelCompleted) {
		early = append(early, e.Fields["asked_early_ms"])
	}
	// The first turn, the kept one, and the follow-up asked with the result.
	if want := []string{"", "0", ""}; !slices.Equal(early, want) {
		t.Errorf("asked_early_ms by completion = %q, want %q", early, want)
	}
	if n := len(eng.heard()); n != 3 {
		t.Errorf("model asked %d times, want 3: the pause's answer was asked again", n)
	}
	if n := len(r.store.ofKind(journal.KindSpeculationDiscarded)); n != 0 {
		t.Errorf("%d discarded speculations, want none", n)
	}
	conv := r.store.ofKind(journal.KindSessionOpened)[0].ConversationID
	st, err := journal.Replay(context.Background(), r.store, conv, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if want := []string{"are the kitchen lights on", "turn them off"}; !slices.Equal(st.Heard, want) {
		t.Errorf("replay heard %v, want %v", st.Heard, want)
	}
	if want := []string{"They are.", "Kitchen lights off."}; !slices.Equal(st.Spoken, want) {
		t.Errorf("replay spoke %v, want %v", st.Spoken, want)
	}
}

// "Turn them off" -- a breath -- "and the hallway too". The guess made in
// the breath is discarded, Home Assistant never hears it, and the whole
// sentence is answered.
//
// verifies SPEC §4.4, §4.5, §8, §11
func TestChorusdDiscardsAGuessThePersonTalksThrough(t *testing.T) {
	r, dev, ha := speculatingDaemon(t)
	eng := r.dm.engine.(*scriptEngine)

	speakFor(t, dev, r.line("turn them off", alan), 8)
	await(t, "the pause to be answered", func() bool { return len(eng.heard()) == 2 })
	r.utter(t, dev, r.line("turn them off and the hallway too", alan))

	d := r.store.awaitKind(t, journal.KindSpeculationDiscarded, 1)
	if !d.Speculative || d.Fields["reason"] != "resumed" || d.Fields["text"] != "turn them off" {
		t.Errorf("discard = %v (speculative %v), want the kitchen guess, resumed", d.Fields, d.Speculative)
	}
	r.store.awaitKind(t, journal.KindModelCompleted, 3)
	// The two calls run side by side, so either may reach the house first.
	got := lightsPosted(ha)
	slices.Sort(got)
	if len(got) != 2 || !strings.Contains(got[0], "light.hallway") || !strings.Contains(got[1], "light.kitchen") {
		t.Errorf("Home Assistant got %v, want only the whole sentence's two calls", got)
	}
	conv := r.store.ofKind(journal.KindSessionOpened)[0].ConversationID
	st, err := journal.Replay(context.Background(), r.store, conv, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if want := []string{"are the kitchen lights on", "turn them off and the hallway too"}; !slices.Equal(st.Heard, want) {
		t.Errorf("replay heard %v, want %v", st.Heard, want)
	}
	if st.Speculative != 1 {
		t.Errorf("replay counted %d speculative events, want the one discard", st.Speculative)
	}
	for _, c := range st.Calls {
		if c.ID == "call_c1" {
			t.Errorf("the discarded guess's call is in the conversation: %+v", c)
		}
	}
}

// CHORUS_SPECULATE is off unless asked for, and a value that is neither on
// nor off is refused rather than guessed.
//
// verifies SPEC §11, §13
func TestTheSpeculationSwitch(t *testing.T) {
	for _, tc := range []struct {
		set     string
		on      bool
		refused bool
	}{
		{set: "", on: false},
		{set: "off", on: false},
		{set: "on", on: true},
		{set: " ON ", on: true},
		{set: "yes", on: true},
		{set: "sometimes", refused: true},
	} {
		got, err := (Config{Speculate: tc.set}).speculate()
		switch {
		case tc.refused && err == nil:
			t.Errorf("%q was accepted", tc.set)
		case !tc.refused && err != nil:
			t.Errorf("%q was refused: %v", tc.set, err)
		case !tc.refused && got != tc.on:
			t.Errorf("%q parsed to %v, want %v", tc.set, got, tc.on)
		}
	}
}
