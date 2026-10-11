package listen_test

import (
	"bytes"
	"math"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
)

// fakeArbiter records every wake entered and decides when the test says.
type fakeArbiter struct {
	verdicts chan listen.Verdict

	mu      sync.Mutex
	entered []listen.Entry
}

func newFakeArbiter() *fakeArbiter { return &fakeArbiter{verdicts: make(chan listen.Verdict, 1)} }

func (f *fakeArbiter) Contend(e listen.Entry) <-chan listen.Verdict {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.Satellite != "kitchen" {
		panic("the rig's listener is the kitchen's: " + e.Satellite)
	}
	f.entered = append(f.entered, e)
	return f.verdicts
}

func (f *fakeArbiter) levels() []float64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []float64
	for _, e := range f.entered {
		out = append(out, e.Energy)
	}
	return out
}

func (f *fakeArbiter) entries() []listen.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]listen.Entry(nil), f.entered...)
}

// arbitrated is the rig with the kitchen's wakes going to arb.
func arbitrated(arb *fakeArbiter) func(*listen.Config) {
	return func(c *listen.Config) { c.Arbiter = arb }
}

// The wake word itself, as the kitchen hears Teagan say it at the counter.
const heyEddie int16 = 6000

// sayWakeWord streams the 600 ms the wake frame's level is measured over,
// then the frame, and waits until the listener has taken it.
func (r *rig) sayWakeWord(t *testing.T, amplitude int16) {
	t.Helper()
	r.speak(t, amplitude, listen.DefaultArbitrationSpan)
	r.dev.SendWake(t, "hey_eddie")
	r.settled(t)
}

// Teagan says "Hey Eddie, turn on the kitchen lights" in one breath. The
// kitchen enters its wake at the level of the 600 ms before the frame,
// and while the contest runs nothing is decoded and nothing opens. Once
// the kitchen has won, the ask is heard whole, from its first word: the
// audio held through the contest is not lost (SPEC §4.5, §9.3).
//
// verifies SPEC §4.5, §9.3
func TestTheWinningSatelliteHearsTheWholeAsk(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))
	lights := r.line("turn on the kitchen lights", alan)

	r.sayWakeWord(t, heyEddie)
	r.utter(t, lights, 8*chunkBytes)
	r.settled(t)

	got := arb.levels()
	want := float64(heyEddie) / 32768
	if len(got) != 1 || math.Abs(got[0]-want) > 1e-9 {
		t.Fatalf("entered %v, want one wake at %.4f, the wake word's level", got, want)
	}
	if n := r.stt.decodes(); n != 0 {
		t.Errorf("%d decodes before the contest was decided", n)
	}
	if r.l.Session() != nil {
		t.Error("a session opened before the contest was decided")
	}

	arb.verdicts <- listen.Verdict{Winner: "kitchen", Won: true, Energy: want, WinnerEnergy: want}
	s := r.session(t)
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)
	if heard.Fields["text"] != "turn on the kitchen lights" || heard.Fields["speaker_id"] != "alan" {
		t.Errorf("utterance_transcribed = %v, want the whole ask by alan", heard.Fields)
	}
	audio, _ := r.blobs.Bytes(heard.AudioRef)
	if !bytes.Contains(audio, voice(lights, 8*chunkBytes)) {
		t.Errorf("the stored audio (%d bytes) does not hold the whole ask", len(audio))
	}
}

// The office heard the same wake louder and answered. The kitchen opens
// nothing, decodes nothing, and records in its own log that it lost to
// the office, at what level against what: an arbitrated wake, with the
// audio it held while the contest ran. What it hears next is nobody's
// turn, since its wake was spent on the contest (SPEC §4.5, §9.3).
//
// verifies SPEC §4.5, §9.3
func TestALosingSatelliteStaysSilentAndSaysWhoWon(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))
	lights := r.line("turn on the kitchen lights", alan)

	r.sayWakeWord(t, 900)
	r.speak(t, lights, 4*chunkBytes)
	r.settled(t)
	arb.verdicts <- listen.Verdict{Winner: "office", Energy: 0.03, WinnerEnergy: 0.3}

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	want := map[string]string{"reason": "arbitrated", "lost_to": "office", "energy_dbfs": "-30.5", "winner_energy_dbfs": "-10.5"}
	for k, v := range want {
		if rej.Fields[k] != v {
			t.Errorf("wake_rejected %s = %q, want %q (fields %v)", k, rej.Fields[k], v, rej.Fields)
		}
	}
	if audio, ok := r.blobs.Bytes(rej.AudioRef); !ok || !bytes.Equal(audio, voice(lights, 4*chunkBytes)) {
		t.Errorf("the rejection's audio is %d bytes, want the 4 chunks held through the contest", len(audio))
	}

	r.pause(t, rigSilence)
	r.utter(t, r.line("and the porch", alan), 8*chunkBytes)
	r.settled(t)
	if r.l.Session() != nil {
		t.Error("the losing satellite opened a session")
	}
	if n := r.stt.decodes(); n != 0 {
		t.Errorf("the losing satellite decoded %d times", n)
	}
	if n := r.count(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected); n != 1 {
		t.Errorf("%d rejections, want the one arbitrated", n)
	}
}

// Teagan mutes the kitchen while its wake waits on the contest. The mute
// is authoritative: the wake is spent with it, so a verdict that arrives
// afterwards opens nothing, and nothing held crosses the mute (CONTRIBUTING §7).
//
// verifies SPEC §4.5
func TestAMuteSpendsAWakeWaitingOnItsVerdict(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))
	lights := r.line("turn on the kitchen lights", alan)

	r.sayWakeWord(t, heyEddie)
	r.speak(t, lights, 4*chunkBytes)
	r.dev.SendMute(t, bridge.Mute{Hardware: true})
	r.settled(t)
	arb.verdicts <- listen.Verdict{Winner: "kitchen", Won: true}
	r.dev.SendMute(t, bridge.Mute{})
	r.utter(t, lights, 8*chunkBytes)
	r.settled(t)

	if r.l.Session() != nil {
		t.Error("a wake spent by the mute opened a session")
	}
	if n := r.stt.decodes(); n != 0 {
		t.Errorf("%d decodes across a mute", n)
	}
}

// Alan is in a conversation with the kitchen and says the wake word again.
// The kitchen still enters it, at its level, so a fainter satellite down
// the hall cannot take the conversation away; whatever the verdict, the
// live session goes on hearing him, and the kitchen records no rejection
// for a wake it had no use for (SPEC §4.5).
//
// verifies SPEC §4.5
func TestAWakeOverALiveSessionStillEntersTheContest(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))
	arb.verdicts <- listen.Verdict{Winner: "kitchen", Won: true}
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 8*chunkBytes)
	s := r.session(t)
	r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)

	r.sayWakeWord(t, heyEddie)
	if n := len(arb.levels()); n != 2 {
		t.Fatalf("%d wakes entered, want the one over the session too", n)
	}
	r.utter(t, r.line("and the second album", alan), 8*chunkBytes)
	r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 2)
	arb.verdicts <- listen.Verdict{Winner: "office", Energy: 0.1, WinnerEnergy: 0.2}
	r.utter(t, r.line("and the third", alan), 8*chunkBytes)
	r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 3)

	if n := r.count(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected); n != 0 {
		t.Errorf("%d rejections for a wake over a live session, want none", n)
	}
	if r.l.Session() != s {
		t.Error("the live session did not go on")
	}
}

// The level is of the processed channel alone, over the span before the
// frame: a loud second channel or older speech does not raise it.
//
// verifies SPEC §4.5
func TestTheLevelIsTheProcessedChannelJustBeforeTheWake(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))

	r.speak(t, 20000, 4*listen.DefaultArbitrationSpan) // the dishwasher, a while ago
	for sent := 0; sent < listen.DefaultArbitrationSpan; sent += chunkBytes {
		r.dev.SendMic(t, bridge.ChannelAEC, voice(heyEddie, chunkBytes))
		r.dev.SendMic(t, bridge.ChannelSecond, voice(30000, chunkBytes))
	}
	r.dev.SendWake(t, "hey_eddie")
	r.settled(t)

	got := arb.levels()
	want := float64(heyEddie) / 32768
	if len(got) != 1 || math.Abs(got[0]-want) > 1e-9 {
		t.Errorf("entered %v, want %.4f: the processed channel over the last %d bytes", got, want, listen.DefaultArbitrationSpan)
	}
}

// Every rejection names the wake its frame carried. Alan's "hey eddie"
// that met only the dishwasher says so; Bob pressing the Voice PE's button
// and saying nothing says button, so the corpus can tell a press from the
// wake model firing. Each wake waits for the one before it to be spent,
// which a window running out does at once (SPEC §9.3).
//
// verifies SPEC §9.3
func TestARejectionNamesTheWakeItsFrameCarried(t *testing.T) {
	r := newRig(t, silent())
	r.dev.SendWake(t, "hey_eddie")
	r.pause(t, rigWake)
	dish := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if dish.Fields["wake_word"] != "hey_eddie" {
		t.Errorf("the dishwasher's wake_word = %q, want hey_eddie", dish.Fields["wake_word"])
	}

	r.dev.SendWake(t, bridge.WakeButton)
	r.pause(t, rigWake)
	press := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 2)
	if press.Fields["reason"] != "no_speech" || press.Fields["wake_word"] != "button" {
		t.Errorf("the silent press = %v, want no_speech from the button", press.Fields)
	}

	r.dev.SendWake(t, bridge.WakeButton)
	r.utter(t, r.line("is anyone home", stranger), 8*chunkBytes)
	guest := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 3)
	if guest.Fields["reason"] != "unknown_speaker" || guest.Fields["wake_word"] != "button" {
		t.Errorf("the guest's press = %v, want unknown_speaker from the button", guest.Fields)
	}
}

// A press of the button is entered as one, and a wake lost to another
// satellite still names its own frame's word.
//
// verifies SPEC §4.5
func TestAPressIsEnteredAsAPress(t *testing.T) {
	arb := newFakeArbiter()
	r := newRig(t, silent(), arbitrated(arb))

	r.speak(t, 900, listen.DefaultArbitrationSpan)
	r.dev.SendWake(t, bridge.WakeButton)
	r.settled(t)
	if got := arb.entries(); len(got) != 1 || !got[0].Pressed {
		t.Fatalf("entered %+v, want one press", got)
	}
	arb.verdicts <- listen.Verdict{Winner: "office", Energy: 0.03, WinnerEnergy: 0.03}
	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "arbitrated" || rej.Fields["wake_word"] != "button" {
		t.Errorf("wake_rejected = %v, want the press arbitrated", rej.Fields)
	}

	r.sayWakeWord(t, heyEddie)
	if got := arb.entries(); len(got) != 2 || got[1].Pressed {
		t.Errorf("entered %+v, want the spoken wake entered as no press", got)
	}
}

// A session records the wake that opened it, as a rejection does: Alan's
// "hey eddie" names the wake model, and Bob's press of the office button
// names the button, so the log tells a spoken wake from a press whichever
// way it ends (SPEC §9.3, ADR-0072).
//
// verifies SPEC §9.3
func TestASessionNamesTheWakeThatOpenedIt(t *testing.T) {
	for _, word := range []string{"hey_eddie", bridge.WakeButton} {
		t.Run(word, func(t *testing.T) {
			r := newRig(t, silent())
			r.dev.SendWake(t, word)
			r.utter(t, r.line("find zeppelin", alan), 8*chunkBytes)

			opened := r.awaitKind(t, r.session(t).ConversationID(), journal.KindSessionOpened, 1)
			if opened.Fields["wake_word"] != word {
				t.Errorf("session_opened wake_word = %q, want %q", opened.Fields["wake_word"], word)
			}
		})
	}
}
