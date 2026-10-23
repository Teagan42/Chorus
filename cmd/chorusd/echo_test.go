package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/bridge/bridgetest"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/echo"
	"github.com/teagan42/chorus/internal/echo/echotest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// The forecast as the household's kitchen said it that morning, in the
// assistant's own Kokoro voice (internal/reviewui/household).
const cloudyForecast = "Today will be cloudy in the morning, with rain from three and a high of fourteen."

// recorded renders text out of one recording of it: each segment the
// satellite asks for is the stretch of the clip its characters span. The
// echo stage compares shapes of a voice, so the audio has to be one.
type recorded struct {
	text string
	pcm  []byte
}

func (k recorded) Synthesize(_ context.Context, seg string) ([]byte, error) {
	at := strings.Index(k.text, seg)
	if at < 0 {
		return nil, fmt.Errorf("the kitchen never said %q", seg)
	}
	frames := len(k.pcm) / 2
	from := frames * at / len(k.text)
	to := frames * (at + len(seg)) / len(k.text)
	return k.pcm[2*from : 2*to], nil
}

// pacedTimers lets the downlink's pacing run -- every wait it asks for is
// under a slice -- and holds everything longer, so no backstop, drain or
// settle ends anything behind the test's back.
type pacedTimers struct{}

func (pacedTimers) After(d time.Duration) <-chan time.Time {
	if d > 200*time.Millisecond {
		return make(chan time.Time)
	}
	fired := make(chan time.Time, 1)
	fired <- time.Time{}
	return fired
}

func cloudyModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		if last := in.Dialogue[len(in.Dialogue)-1]; last.Kind == journal.EntryHeard && last.Text == "what's the weather today" {
			return []session.Action{session.SpeechDelta{CallID: "call_w1", Text: cloudyForecast, Last: true}, done}
		}
		return []session.Action{done}
	}}
}

// cloudyKitchen is the household with the kitchen speaking in its real
// voice, under inv: the inventory, which a test may give its own settings.
func cloudyKitchen(t *testing.T, inv *config.Config) (*rig, *scriptEngine, *bridgetest.Device, []byte) {
	t.Helper()
	played := echotest.Clip(t, "tts/weather-forecast")
	eng := cloudyModel()
	emb := newEmbedder()
	r := newRig(t, inv, func(d *deps) {
		d.engine = eng
		d.household = []string{"alan", "teagan", "alice"}
		d.speakers = threeVoices(t, emb)
		d.synth = recorded{text: cloudyForecast, pcm: played}
		d.Timers = pacedTimers{}
	})
	r.emb = emb
	r.stt.mu.Lock()
	r.stt.otherwise = "today will be cloudy"
	r.stt.mu.Unlock()
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the weather today", teagan))
	dev.AwaitTTS(t, len(played))
	return r, eng, dev, played
}

// echoOver plays the forecast on the device and sends what its microphone
// hears of it, a chunk at a time: PLAYED, then the mic, as the firmware's
// loop does. Paced on the gate: the next chunk goes once every partial
// decoded so far has been judged, as a real mic's 32 ms a chunk allows, or
// the decodes coalesce into one candidate for seconds of audio.
func echoOver(t *testing.T, r *rig, dev *bridgetest.Device, uplink []byte) {
	t.Helper()
	before := r.stt.decodes()
	judged := func() int {
		return len(r.store.ofKind(journal.KindBargeInRejected)) + len(r.store.ofKind(journal.KindBargeInDetected))
	}
	offered := judged()
	for off := 0; off < len(uplink); off += chunkBytes {
		chunk := uplink[off:min(off+chunkBytes, len(uplink))]
		dev.Play(t, uint64(len(chunk)/2))
		dev.SendMic(t, bridge.ChannelAEC, chunk)
		await(t, "the gate to judge every partial", func() bool {
			return len(r.store.ofKind(journal.KindBargeInDetected)) > 0 || judged()-offered >= r.stt.decodes()-before
		})
	}
}

// Teagan asks the kitchen for the weather, and the kitchen's own forecast
// comes back through its microphone the whole time it speaks, 140 ms late
// and brought up by the AGC to where her voice would be. The ear reads words
// in it. None of it stops the forecast: the gate refuses it at the echo
// stage -- against what the satellite sent the DAC, on the DAC's own count --
// with its score kept for tuning; the forecast plays to its end, no stop is
// sent, and the echo is never answered as anyone's turn.
//
// verifies SPEC §3.2.1, §4.3
func TestTheKitchenHearsItsOwnForecastAndTalksOn(t *testing.T) {
	r, eng, dev, played := cloudyKitchen(t, inventory())

	echoOver(t, r, dev, echotest.Kitchen.Echo(played)[:2*3*echotest.Rate])
	await(t, "the echo refused at the echo stage", func() bool {
		for _, e := range r.store.ofKind(journal.KindBargeInRejected) {
			if e.Fields["stage"] == "echo" {
				return true
			}
		}
		return false
	})
	hush(t, dev)
	dev.PlayAll(t)

	if spoken := r.spoken(t, "kitchen", 1); spoken.Fields["text"] != cloudyForecast {
		t.Errorf("speech_spoken = %v, want the whole forecast", spoken.Fields)
	}
	if n := dev.Stops(); n != 0 {
		t.Errorf("%d stops sent for the kitchen's own voice", n)
	}
	if n := len(r.store.ofKind(journal.KindBargeInDetected)); n != 0 {
		t.Errorf("the forecast stopped itself %d times", n)
	}
	for _, e := range r.store.ofKind(journal.KindBargeInRejected) {
		if e.Fields["stage"] != "echo" {
			continue
		}
		score, _ := strconv.ParseFloat(e.Fields["echo_score"], 64)
		lag, _ := strconv.Atoi(e.Fields["echo_lag_ms"])
		if score < echo.DefaultThreshold || lag < 120 || lag > 160 {
			t.Errorf("barge_in_rejected = %v, want a score over the threshold near the room's 140 ms", e.Fields)
		}
	}
	r.utter(t, dev, r.line("thanks", teagan))
	await(t, "Teagan's thanks", func() bool { return len(eng.heard()) >= 2 })
	if got := askedTexts(eng); !slices.Equal(got, []string{"what's the weather today", "thanks"}) {
		t.Errorf("the model was asked %q, want never the echo", got)
	}
}

// devices.yaml's echo_threshold reaches the kitchen's gate: above 1 nothing
// scores, so the stage is off, and the same echo is refused where it was
// before the stage existed -- at the speaker stage, an embedding each.
//
// verifies SPEC §4.3, §13
func TestTheKitchensEchoThresholdIsItsOwn(t *testing.T) {
	inv := inventory()
	inv.Satellites[0].EchoThreshold = 1.5
	r, _, dev, played := cloudyKitchen(t, inv)

	echoOver(t, r, dev, echotest.Kitchen.Echo(played)[:2*2*echotest.Rate])
	rej := r.store.awaitKind(t, journal.KindBargeInRejected, 2)
	hush(t, dev)
	if rej.Fields["stage"] != "speaker_id" {
		t.Errorf("barge_in_rejected = %v, want the speaker stage with the echo stage off", rej.Fields)
	}
	// Still compared, and still saying so: the threshold is what tunes it.
	if score, err := strconv.ParseFloat(rej.Fields["echo_score"], 64); err != nil || score < echo.DefaultThreshold {
		t.Errorf("echo_score = %q, want the echo's score kept with the stage off", rej.Fields["echo_score"])
	}
}
