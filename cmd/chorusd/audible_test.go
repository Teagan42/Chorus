package main

import (
	"context"
	"errors"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/pb"
	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
)

// deadKokoro is a Kokoro container that is still pulling its image when the
// daemon starts: nothing it is asked ever renders.
type deadKokoro struct{}

func (deadKokoro) Synthesize(context.Context, string) ([]byte, error) {
	return nil, errors.New(`kokoro: Post "http://10.0.0.21:8880/v1/audio/speech": connect: connection refused`)
}

// builtIn is how many bytes of audio a canned line is said in when nothing
// has rendered it: the segments built into the binary.
func builtIn(line string) int {
	n := 0
	for _, part := range satellite.Segments(line) {
		pcm, _ := satellite.Fallback(part)
		n += len(pcm)
	}
	return n
}

// The whole house comes up after a power cut, and chorusd is ready before
// Kokoro or Ollama. Alan asks for the lights: the kitchen still says it
// cannot think, from the audio built into the binary, rather than nothing.
//
// verifies SPEC §7
func TestTheApologyPlaysWhileKokoroIsStillStarting(t *testing.T) {
	r := newRig(t, inventory(), func(d *deps) {
		d.synth = deadKokoro{}
		d.engine = refusingEngine{}
		d.Timers = pacingOnly{}
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn off the kitchen lights", alan))

	dev.AwaitTTS(t, builtIn(session.DefaultCanned.Model))
	dev.PlayAll(t)
	spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	failed := r.store.awaitKind(t, journal.KindModelFailed, 1)
	if spoken.Fields["text"] != session.DefaultCanned.Model || spoken.Fields["call_id"] != failed.Fields["canned_call_id"] {
		t.Errorf("speech_spoken = %v, want the model's apology, whole", spoken.Fields)
	}
	if n := len(r.store.ofKind(journal.KindSpeechFailed)); n != 0 {
		t.Errorf("%d speech failures: the built-in apology should not need Kokoro", n)
	}
	r.logs.await(t, "built into the binary")
}

// fullRing is the ring esphome/satellite1.yaml declares now: Chorus's three
// session effects, and one each for the mute and a failure.
func fullRing() *pb.ListEntitiesLightResponse {
	r := chorusRing()
	r.Effects = append(r.Effects, "Muted", "Error")
	return r
}

// Bob presses the kitchen's mute. The ring shows it until he unmutes.
//
// verifies SPEC §3.3.1
func TestTheKitchenRingShowsTheMute(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })
	conn := connect(t, dialer)
	listing(t, conn, fullRing())
	ringShows(t, conn, "")

	dev := r.join(t, kitchenIP)
	dev.SendMute(t, bridge.Mute{Hardware: true})
	ringShows(t, conn, "Muted")
	dev.SendMute(t, bridge.Mute{})
	ringShows(t, conn, "")
}

// Alan wakes the kitchen with the speech-to-text sidecar down. The kitchen
// says it cannot hear, and the ring shows the failure once it has.
//
// verifies SPEC §3.3.1, §7
func TestTheKitchenRingShowsAnErrorWhenTheEarIsDown(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) {
		// The apology plays through the downlink's pacing at once.
		d.Native, d.Clock, d.Timers = dialer, clk, pacingOnly{}
		d.stt = downSTT{}
	})
	conn := connect(t, dialer)
	listing(t, conn, fullRing())
	ringShows(t, conn, "")

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("turn on the porch light", alan))
	dev.AwaitTTS(t, 2*len(session.DefaultCanned.Ear))
	ringShows(t, conn, "Speaking")
	dev.PlayAll(t)
	ringShows(t, conn, "Error")

	rej := r.store.awaitKind(t, journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "transcription_failed" || rej.Fields["canned_call_id"] == "" {
		t.Errorf("wake_rejected = %v, want transcription_failed naming its apology", rej.Fields)
	}
}

// The office still runs firmware whose ring has only the three session
// effects. Muting it mid-conversation turns the ring dark, since it has no
// way to say muted and listening would be a lie; unmuting shows the session
// again.
//
// verifies SPEC §3.3.1
func TestARingWithOnlyTheSessionEffectsKeepsWorking(t *testing.T) {
	inv := &config.Config{Satellites: []config.Satellite{inventory().Satellites[0]}}
	dialer, clk := newDialer(), newClock()
	r := newRig(t, inv, func(d *deps) { d.Native, d.Clock, d.Timers = dialer, clk, clk })
	conn := connect(t, dialer)
	listing(t, conn, chorusRing())
	ringShows(t, conn, "")

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("is the garage door shut", alan))
	ringShows(t, conn, "Listening")

	dev.SendMute(t, bridge.Mute{Software: true})
	ringShows(t, conn, "")
	dev.SendMute(t, bridge.Mute{})
	ringShows(t, conn, "Listening")
}
