package listen_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
)

// lighter is the XMOS's second output under the same voice: quieter, as a
// stream without AGC is. No line in any test speaks at it.
const lighter int16 = 3100

// both streams n bytes on each channel, a chunk at a time and interleaved,
// as the device sends them: the first channel at amplitude, the second at
// lighter whenever the first is voiced.
func (r *rig) both(t *testing.T, amplitude int16, n int) {
	t.Helper()
	for sent := 0; sent < n; sent += chunkBytes {
		size := min(chunkBytes, n-sent)
		first, second := quiet(size), quiet(size)
		if amplitude != 0 {
			first, second = voice(amplitude, size), voice(lighter, size)
		}
		r.dev.SendMic(t, bridge.ChannelAEC, first)
		r.dev.SendMic(t, bridge.ChannelSecond, second)
	}
}

// Both channels of an utterance are kept: the first is what was decoded,
// and the second, over the same span, rides beside it for retraining on
// either stream (SPEC §8, §9.3). Nothing decides on the second: neither
// recognition, nor the speaker, nor where the utterance ends.
//
// verifies SPEC §8, §9.3
func TestTheSecondChannelIsKeptBesideTheUtterance(t *testing.T) {
	r := newRig(t, silent())
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.both(t, lights, 6*chunkBytes)
	r.both(t, 0, rigSilence)

	s := r.session(t)
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)
	if heard.Fields["text"] != "turn off the kitchen lights" {
		t.Errorf("heard %q", heard.Fields["text"])
	}
	first, ok := r.blobs.Bytes(heard.AudioRef)
	if !ok {
		t.Fatalf("no blob at %q", heard.AudioRef)
	}
	if got := voiced(first, lighter); got != 0 {
		t.Errorf("%d bytes of the second channel leaked into what was decoded", got)
	}
	ref := heard.Fields["second_audio_ref"]
	second, ok := r.blobs.Bytes(ref)
	if !ok {
		t.Fatalf("second_audio_ref %q is not in the blob store", ref)
	}
	if got, want := voiced(second, lighter), voiced(first, lights); got != want {
		t.Errorf("the second channel holds %d voiced bytes, the first %d: not the same span", got, want)
	}
	if got := voiced(second, lights); got != 0 {
		t.Errorf("%d bytes of the first channel landed in the second", got)
	}
}

// A wake nobody spoke after is the hard negative §9.3 is for, so its
// window is kept from both channels. The two line up to the chunk: the
// window closes on the first channel's last chunk, before the second's.
//
// verifies SPEC §9.3
func TestARejectedWakeKeepsBothChannels(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.both(t, 0, rigWake)

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	first, _ := r.blobs.Bytes(rej.AudioRef)
	second, ok := r.blobs.Bytes(rej.Fields["second_audio_ref"])
	if !ok {
		t.Fatalf("second_audio_ref %q is not in the blob store", rej.Fields["second_audio_ref"])
	}
	if d := len(first) - len(second); d < 0 || d > chunkBytes {
		t.Errorf("the second channel holds %d bytes of the window, the first %d: more than a chunk apart", len(second), len(first))
	}
}

// The television saying the wake word is rejected at stage three, and its
// second channel is kept with it, from the same utterance.
//
// verifies SPEC §9.3
func TestAnUnknownVoiceIsRejectedWithBothChannels(t *testing.T) {
	r := newRig(t, silent())
	news := r.line("and now the weather", stranger)

	r.dev.SendWake(t, "hey_eddie")
	r.both(t, news, 4*chunkBytes)
	r.both(t, 0, rigSilence)

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "unknown_speaker" {
		t.Errorf("reason = %q, want unknown_speaker", rej.Fields["reason"])
	}
	second, ok := r.blobs.Bytes(rej.Fields["second_audio_ref"])
	if !ok || voiced(second, lighter) != 4*chunkBytes {
		t.Errorf("second channel = %d voiced bytes (stored %v), want the 4 chunks spoken", voiced(second, lighter), ok)
	}
}

// A device that streams one channel, as a Voice PE wired for one can,
// records no second: the field is absent, not a reference to nothing.
//
// verifies SPEC §8
func TestOneChannelRecordsNoSecond(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("what's on the calendar", alan), 4*chunkBytes)

	heard := r.awaitKind(t, r.session(t).ConversationID(), journal.KindUtteranceTranscribed, 1)
	if ref, ok := heard.Fields["second_audio_ref"]; ok {
		t.Errorf("second_audio_ref = %q from a device that sent one channel", ref)
	}
}

// Hardware mute is authoritative on every channel (CONTRIBUTING §7): the
// second channel under it is dropped, and none of it reaches the next
// utterance's corpus.
//
// verifies SPEC §3.2
func TestMuteDropsTheSecondChannelToo(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.both(t, r.line("find zeppelin", alan), 4*chunkBytes)
	r.both(t, 0, rigSilence)
	conv := r.session(t).ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)

	r.dev.SendMute(t, bridge.Mute{Hardware: true})
	for range 8 {
		r.dev.SendMic(t, bridge.ChannelSecond, voice(lighter-1, chunkBytes))
	}
	r.dev.SendMute(t, bridge.Mute{})
	r.both(t, r.line("play the next one", alan), 4*chunkBytes)
	r.both(t, 0, rigSilence)

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	second, _ := r.blobs.Bytes(heard.Fields["second_audio_ref"])
	if got := voiced(second, lighter-1); got != 0 {
		t.Errorf("%d bytes of the second channel from under the mute were kept", got)
	}
	if got := voiced(second, lighter); got != 4*chunkBytes {
		t.Errorf("the second channel holds %d voiced bytes after unmute, want %d", got, 4*chunkBytes)
	}
}
