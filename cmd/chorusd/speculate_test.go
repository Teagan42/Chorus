package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/provider/vllm"
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

// The same evening on the vLLM engine, whose speak calls stream their words
// out of the arguments: Alan greets the kitchen, then asks for the kitchen
// and porch lights off. vLLM is asked in the pause, its stream is held
// until the utterance ends, and then plays out as if it were arriving --
// both lights switched, the words heard, and vLLM asked once more with the
// results. A real engine on a recorded stream, through the whole daemon.
//
// verifies SPEC §4.1, §11, §12
func TestAKeptSpeculationStreamsFromVLLM(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "..", "internal", "provider", "vllm", "testdata", name))
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		return b
	}
	rec := &recordedVLLM{streams: [][]byte{read("reply.sse"), read("speak_and_two_lights.sse"), read("done_and_end.sse")}}
	eng, err := vllm.New(vllm.Config{BaseURL: "http://10.0.0.20:8000", Model: "Qwen/Qwen3-32B-AWQ", HTTP: &http.Client{Transport: rec}})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	var mu sync.Mutex
	var switched []string
	lights := session.ToolFunc(func(_ context.Context, args string) (string, error) {
		var a struct {
			EntityID string `json:"entity_id"`
		}
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return "", err
		}
		mu.Lock()
		switched = append(switched, a.EntityID)
		mu.Unlock()
		return `{"changed":["` + a.EntityID + `"]}`, nil
	})
	r := newRig(t, inventory(), func(d *deps) {
		d.engine, d.summarizer, d.versions = eng, nil, eng.Versions()
		d.tools = map[string]session.Tool{"ha_call_service": lights}
		d.Timers = wallTimers{}
		d.speculate = true
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("hello there", alan))
	// Settled, not heard: on this engine a lone short clause is sometimes
	// recorded with no speech_spoken, with speculation on or off.
	await(t, "the first turn to settle", func() bool {
		dev.PlayAll(t)
		if len(r.store.ofKind(journal.KindToolResult)) < 1 {
			return false
		}
		s := r.dm.linked("kitchen").Session()
		return s != nil && slices.Equal(s.Children(), []string{"listening"})
	})

	speakFor(t, dev, r.line("turn off the kitchen and porch lights", alan), 8)
	await(t, "vLLM to be asked in the pause", func() bool {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.sent) == 2
	})
	if n := len(r.store.ofKind(journal.KindUtteranceTranscribed)); n != 1 {
		t.Fatalf("%d utterances heard when vLLM was asked, want only the greeting", n)
	}
	mu.Lock()
	if len(switched) != 0 {
		t.Errorf("lights switched in the pause: %v", switched)
	}
	mu.Unlock()
	for sent := listen.DefaultPause; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}

	var heard []string
	await(t, "both answers to be heard", func() bool {
		dev.PlayAll(t)
		heard = nil
		for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
			if e.Fields["text"] != "Hello!" {
				heard = append(heard, e.Fields["text"])
			}
		}
		return len(heard) >= 2
	})
	if want := []string{"Turning off the kitchen and porch lights.", "Done, both are off."}; !slices.Equal(heard, want) {
		t.Errorf("speech_spoken = %q, want %q", heard, want)
	}
	mu.Lock()
	slices.Sort(switched)
	if !slices.Equal(switched, []string{"light.kitchen", "light.porch"}) {
		t.Errorf("lights switched = %v", switched)
	}
	mu.Unlock()
	r.store.awaitKind(t, journal.KindModelCompleted, 3)
	completed := r.store.ofKind(journal.KindModelCompleted)
	if completed[1].Fields["asked_early_ms"] == "" || completed[1].Fields["streamed_speech_json"] == "" {
		t.Errorf("the kept completion = %v, want it asked early with its streamed words", completed[1].Fields)
	}
	if n := len(r.store.ofKind(journal.KindSpeculationDiscarded)); n != 0 {
		t.Errorf("%d discarded speculations, want none", n)
	}
	// The speculative request is the one a late ask would have sent: it
	// ends on the words the utterance was confirmed as.
	var asked struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(rec.request(t, 1), &asked); err != nil {
		t.Fatal(err)
	}
	if last := asked.Messages[len(asked.Messages)-1]; last.Role != "user" || last.Content != "turn off the kitchen and porch lights" {
		t.Errorf("the pause asked vLLM with %+v last", last)
	}
}
