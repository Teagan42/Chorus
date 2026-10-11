package main

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

// The long weather answer the kitchen gives Teagan in the morning.
const morningForecast = "Sunny this morning, then rain from three."

// Teagan's voice; Alice's is confirm_test.go's.
var teagan = axis(2)

// threeVoices enrolls Alan, Teagan and Alice, as the household's resolver.
func threeVoices(t *testing.T, emb identity.Embedder) *identity.Resolver {
	t.Helper()
	ids := identity.New(emb)
	for _, p := range []struct {
		id, name string
		v        []float32
	}{{"alan", "Alan", alan}, {"teagan", "Teagan", teagan}, {"alice", "Alice", alice}} {
		if err := ids.Enroll(p.id, p.name, [][]float32{p.v, p.v, p.v}); err != nil {
			t.Fatalf("enroll %s: %v", p.id, err)
		}
	}
	r, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	return r
}

// weatherModel answers the weather with the long forecast, and the
// television's ad break or anything else with nothing to say.
func weatherModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		if last := in.Dialogue[len(in.Dialogue)-1]; last.Kind == journal.EntryHeard && last.Text == "what's the weather today" {
			return []session.Action{session.SpeechDelta{CallID: "call_w1", Text: morningForecast, Last: true}, done}
		}
		return []session.Action{done}
	}}
}

// newHousehold is the rig with Teagan and Alice enrolled beside Alan.
func newHousehold(t *testing.T, eng *scriptEngine, tweak ...func(*deps)) *rig {
	t.Helper()
	emb := newEmbedder()
	r := newRig(t, inventory(), append([]func(*deps){func(d *deps) {
		d.engine = eng
		d.household = []string{"alan", "teagan", "alice"}
		d.speakers = threeVoices(t, emb)
	}}, tweak...)...)
	// Lines are scripted into the embedder the resolver asks.
	r.emb = emb
	return r
}

// say streams enough of a voice for the ear to judge a partial, then stops
// talking, so the utterance ends.
func say(t *testing.T, dev *bridgetest.Device, amplitude int16) {
	t.Helper()
	sayOver(t, dev, amplitude)
	for sent := 0; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
}

// sayOver streams enough of a voice for a partial, still talking.
func sayOver(t *testing.T, dev *bridgetest.Device, amplitude int16) {
	t.Helper()
	for sent := 0; sent < 2*stt.DefaultPartialEvery; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, voice(amplitude, chunkBytes))
	}
}

// askedTexts is what the model was asked, in order.
func askedTexts(eng *scriptEngine) []string {
	var out []string
	for _, in := range eng.heard() {
		out = append(out, in.Text)
	}
	return out
}

// Teagan, in the kitchen, says "stop" over the long weather answer. One
// word stops it where her ear was: the cut is the report that answers the
// stop (ADR-0033), the detection names the phrase, and nobody asks the
// model what to say to "stop". Her next question is the next thing asked.
//
// verifies SPEC §4.3, §4.4
func TestTeaganSaysStopOverTheForecast(t *testing.T) {
	eng := weatherModel()
	r := newHousehold(t, eng)
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather today", teagan))

	dev.AwaitTTS(t, 2*len(morningForecast))
	heard := dev.Play(t, 10)
	dev.PlayDuringStop(6)
	say(t, dev, r.line("Stop.", teagan))

	dev.AwaitStop(t, 1)
	cut := r.store.awaitKind(t, journal.KindSpeechTruncated, 1)
	if cut.Fields["frames_played"] != strconv.FormatUint(heard+6, 10) || cut.Fields["reason"] != "barge_in" ||
		!strings.HasPrefix(morningForecast, cut.Fields["spoken_text"]) {
		t.Errorf("speech_truncated = %v, want the forecast cut at frame %d by Teagan", cut.Fields, heard+6)
	}
	if det := r.store.awaitKind(t, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "stop" {
		t.Errorf("barge_in_detected = %v, want the stop", det.Fields)
	}
	stop := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2)
	if stop.Fields["text"] != "Stop." || stop.Fields["hot_word"] != "stop" || stop.Fields["speaker_id"] != "teagan" {
		t.Errorf("utterance_transcribed = %v, want Teagan's stop", stop.Fields)
	}

	r.utter(t, dev, r.line("what time is it", teagan))
	await(t, "Teagan's next question", func() bool { return len(eng.heard()) == 2 })
	if got := askedTexts(eng); !slices.Equal(got, []string{"what's the weather today", "what time is it"}) {
		t.Errorf("the model was asked %q, want never the stop", got)
	}
}

// The television says "stop" over the forecast. It is loud and it is a
// hot phrase, and nobody in the house: nothing is stopped, the forecast
// plays to its end, and the television is never asked about.
//
// verifies SPEC §4.3, §5
func TestTheTelevisionSaysStopAndNothingHappens(t *testing.T) {
	eng := weatherModel()
	r := newHousehold(t, eng)
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather today", teagan))
	dev.AwaitTTS(t, 2*len(morningForecast))
	dev.Play(t, 10)

	say(t, dev, r.line("Stop!", stranger))
	rej := r.store.awaitKind(t, journal.KindBargeInRejected, 1)
	if rej.Fields["stage"] != "speaker_id" || rej.Fields["hot_word"] != "stop" {
		t.Errorf("barge_in_rejected = %v, want the television's stop at speaker_id", rej.Fields)
	}
	dev.PlayAll(t)
	if spoken := r.spoken(t, "kitchen", 1); spoken.Fields["text"] != morningForecast {
		t.Errorf("speech_spoken = %v, want the whole forecast", spoken.Fields)
	}
	if n := dev.Stops(); n != 0 {
		t.Errorf("%d stops sent for the television", n)
	}
	if n := len(r.store.ofKind(journal.KindBargeInDetected)); n != 0 {
		t.Errorf("the television stopped speech %d times", n)
	}
	r.utter(t, dev, r.line("thanks", teagan))
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2)
	for _, e := range r.store.ofKind(journal.KindUtteranceTranscribed) {
		if e.Fields["speaker_id"] != "teagan" {
			t.Errorf("heard %v: the television is not a turn", e.Fields)
		}
	}
}

// Teagan asked whether the garage is shut, and the kitchen says it is
// looking while it reads the cover. "Never mind" stops it saying so and
// gives up the read, which a barge-in cancels, and is not answered.
//
// verifies SPEC §4.3, §4.4
func TestTeaganSaysNeverMindWhileTheKitchenLooks(t *testing.T) {
	const looking = "Let me look at the garage, one moment."
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		if last := in.Dialogue[len(in.Dialogue)-1]; last.Kind == journal.EntryHeard && last.Text == "is the garage shut" {
			return []session.Action{
				session.SpeechDelta{CallID: "call_s1", Text: looking, Last: true},
				session.ToolCall{ID: "call_c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
				done,
			}
		}
		return []session.Action{done}
	}}
	r := newHousehold(t, eng, func(d *deps) {
		d.tools = map[string]session.Tool{"ha_get_state": session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		})}
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("is the garage shut", teagan))
	dev.AwaitTTS(t, 2*len(looking))
	dev.Play(t, 10)

	say(t, dev, r.line("Never mind.", teagan))
	dev.AwaitStop(t, 1)
	if det := r.store.awaitKind(t, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "never_mind" {
		t.Errorf("barge_in_detected = %v, want never mind", det.Fields)
	}
	await(t, "the garage read to end", func() bool {
		for _, e := range r.store.ofKind(journal.KindToolResult) {
			if e.Fields["call_id"] == "call_c1" {
				return e.Fields["outcome"] == "cancelled"
			}
		}
		return false
	})
	if heard := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2); heard.Fields["hot_word"] != "never_mind" {
		t.Errorf("utterance_transcribed = %v, want never mind", heard.Fields)
	}
	r.utter(t, dev, r.line("what's the weather today", teagan))
	await(t, "Teagan's next question", func() bool { return len(eng.heard()) == 2 })
	if got := askedTexts(eng); got[1] != "what's the weather today" {
		t.Errorf("the model was asked %q, want never the never mind", got)
	}
}

// Alice hears the whole forecast and asks the kitchen to say that again.
// It says the same words through the same speaker, as a speak call it made
// itself: the model is not asked, and the log says it was a repeat.
//
// verifies SPEC §4.2, §4.3
func TestAliceSaysSayThatAgain(t *testing.T) {
	eng := weatherModel()
	r := newHousehold(t, eng)
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather today", alice))
	dev.AwaitTTS(t, 2*len(morningForecast))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)

	r.utter(t, dev, r.line("Say that again?", alice))
	dev.AwaitTTS(t, 4*len(morningForecast))
	dev.PlayAll(t)
	again := r.spoken(t, "kitchen", 2)
	if again.Fields["text"] != morningForecast || !strings.HasPrefix(again.Fields["call_id"], "rp_") {
		t.Errorf("speech_spoken = %v, want the forecast said again", again.Fields)
	}
	if heard := r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2); heard.Fields["hot_word"] != "repeat" {
		t.Errorf("utterance_transcribed = %v, want a repeat", heard.Fields)
	}
	if got := askedTexts(eng); !slices.Equal(got, []string{"what's the weather today"}) {
		t.Errorf("the model was asked %q, want only the weather", got)
	}
}

// The bread timer Alan sets in the kitchen, and what it says going off.
const (
	breadDone = "The bread timer is done, take it out now."
	breadSet  = "Forty minutes on the bread."
	setBread  = `{"seconds":2400,"label":"bread","announcement":"` + breadDone + `"}`
)

// breadModel sets the bread timer when asked, and ends the conversation.
func breadModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "set a timer for forty minutes for the bread":
			return []session.Action{session.ToolCall{ID: "call_t1", Tool: "timer_start", Args: setBread}, done}
		case last.Kind == journal.EntryResult && last.Tool == "timer_start":
			return []session.Action{
				session.SpeechDelta{CallID: "call_s1", Text: breadSet, Last: true},
				session.ToolCall{ID: "call_e1", Tool: "end_session", Args: "{}"},
				done,
			}
		}
		return []session.Action{done}
	}}
}

// Forty minutes after Alan set it, the bread timer goes off in the empty
// kitchen, in a session nobody may answer. Alan, at the oven, says "stop":
// the kitchen stops where his ear was, the house log says the timer was
// heard, and nothing Alan said is a turn.
//
// verifies SPEC §4, §4.2, §4.3
func TestAlanStopsTheBreadTimerGoingOff(t *testing.T) {
	clk := newClock()
	r := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Clock, d.Timers = breadModel(), clk, clk
	})
	kitchen := r.join(t, kitchenIP)
	kitchen.SendWake(t, "hey_eddie")
	r.utter(t, kitchen, r.line("set a timer for forty minutes for the bread", alan))
	r.store.awaitKind(t, journal.KindTimerStarted, 1)
	kitchen.AwaitTTS(t, 2*len(breadSet))
	kitchen.PlayAll(t)
	r.store.awaitKind(t, journal.KindSessionClosed, 1)

	clk.advance(40 * time.Minute)
	opened := r.store.awaitKind(t, journal.KindSessionOpened, 2)
	kitchen.AwaitTTS(t, 2*len(breadSet)+2*len(breadDone))
	heard := kitchen.Play(t, 10)
	say(t, kitchen, r.line("stop", alan))

	kitchen.AwaitStop(t, 1)
	conv := opened.ConversationID
	cut := r.store.awaitKind(t, journal.KindSpeechTruncated, 1)
	want := map[string]string{
		"spoken_text": "The bread timer is done, ", "unspoken_text": "take it out now.",
		"frames_played": strconv.FormatUint(heard, 10), "reason": "barge_in",
	}
	for k, v := range want {
		if cut.Fields[k] != v {
			t.Errorf("speech_truncated %s = %q, want %q", k, cut.Fields[k], v)
		}
	}
	if cut.ConversationID != conv {
		t.Errorf("the cut is in %s, want the timer's %s", cut.ConversationID, conv)
	}
	if det := r.store.awaitKind(t, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "stop" || det.ConversationID != conv {
		t.Errorf("barge_in_detected = %v, want Alan's stop", det.Fields)
	}
	if finished := r.store.awaitKind(t, journal.KindTimerFinished, 1); finished.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v, want announced: Alan heard it", finished.Fields)
	}
	await(t, "the timer's session to close", func() bool {
		events, _ := r.store.Events(context.Background(), conv)
		return events[len(events)-1].Kind == journal.KindSessionClosed
	})
	events, _ := r.store.Events(context.Background(), conv)
	if closed := events[len(events)-1]; closed.Fields["reason"] != "announced" {
		t.Errorf("closed as %v, want announced", closed.Fields)
	}
	for _, e := range events {
		if e.Kind == journal.KindUtteranceTranscribed {
			t.Errorf("heard %v over the timer", e.Fields)
		}
	}
	if n := len(r.store.ofKind(journal.KindUtteranceTranscribed)); n != 1 {
		t.Errorf("%d utterances heard, want only Alan setting the timer", n)
	}
}
