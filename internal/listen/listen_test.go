package listen_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

const line = "I found three albums by that artist"

func talking() []step {
	return []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
}

func silent() []step {
	return []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
}

// The wire carries no pre-roll (bridge.TypeWake is the word alone), so the
// person is whoever the first utterance turns out to be: the session opens on
// that satellite for that person, keyed on them (SPEC §4.5), and the utterance
// is heard with its speaker, its audio and its embedding (SPEC §5).
//
// verifies SPEC §4.5, §5, §9.3
func TestWakeThenSpeechOpensTheSessionForTheSpeaker(t *testing.T) {
	r := newRig(t, silent())
	zep := r.line("find zeppelin", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, zep, 8*chunkBytes)

	s := r.session(t)
	conv := s.ConversationID()
	opened := r.awaitKind(t, conv, journal.KindSessionOpened, 1)
	if opened.Fields["satellite"] != "kitchen" || opened.Fields["speaker_id"] != "alan" {
		t.Errorf("session_opened = %v, want kitchen/alan", opened.Fields)
	}
	if _, scored := opened.Fields["wake_confidence"]; scored {
		t.Error("a wake nothing re-scored carries a confidence")
	}

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	if heard.Fields["text"] != "find zeppelin" || heard.Fields["speaker_id"] != "alan" {
		t.Errorf("utterance_transcribed = %v, want find zeppelin by alan", heard.Fields)
	}
	if heard.Fields["embedding_json"] != "[1,0,0,0]" {
		t.Errorf("embedding_json = %q, want alan's vector", heard.Fields["embedding_json"])
	}
	audio, ok := r.blobs.Bytes(heard.AudioRef)
	if !ok {
		t.Fatalf("no blob at %q", heard.AudioRef)
	}
	if !bytes.Contains(audio, voice(zep, 8*chunkBytes)) {
		t.Errorf("the stored audio (%d bytes) does not hold the utterance", len(audio))
	}
	if len(audio)%2 != 0 {
		t.Errorf("stored %d bytes, not whole samples", len(audio))
	}

	await(t, "the turn", func() bool { return len(r.engine.heard()) == 1 })
	if in := r.engine.heard()[0]; in.Text != "find zeppelin" || in.Speaker != "alan" {
		t.Errorf("the model was given %+v", in)
	}
	if got := r.count(t, conv, journal.KindUtteranceTranscribed); got != 1 {
		t.Errorf("%d utterances transcribed, want exactly one", got)
	}
}

// A cough fires the on-device model; the orchestrator must keep it invisible.
// With no pre-roll on the wire the segment judged is the first utterance: one
// STT hears no words in is a hard negative, journalled with its audio to the
// device's own log, and no session ever opens.
//
// verifies SPEC §9.3
func TestAWakeWithNoSpeechIsRejectedInvisibly(t *testing.T) {
	r := newRig(t, silent())
	cough := r.line("", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, cough, 4*chunkBytes)

	log := listen.DeviceConversation("kitchen")
	rej := r.awaitKind(t, log, journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "no_speech" {
		t.Errorf("reason = %q, want no_speech", rej.Fields["reason"])
	}
	if _, ok := r.blobs.Bytes(rej.AudioRef); !ok {
		t.Errorf("the hard negative's audio %q is not stored", rej.AudioRef)
	}
	if r.l.Session() != nil {
		t.Error("a rejected wake opened a session")
	}

	// The wake is spent: more speech is nobody's until the next activation.
	r.utter(t, r.line("turn off the lights", alan), 4*chunkBytes)
	r.settled(t)
	if r.l.Session() != nil {
		t.Error("speech after a rejected wake opened a session")
	}
}

// A wake followed by nothing at all is the same cough: the window of audio
// after it expires, in bytes, and is journalled as the negative.
//
// verifies SPEC §9.3
func TestAWakeFollowedBySilenceExpires(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.pause(t, rigWake)

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "no_speech" {
		t.Errorf("reason = %q, want no_speech", rej.Fields["reason"])
	}
	if r.stt.decodes() != 0 {
		t.Errorf("%d decodes of silence", r.stt.decodes())
	}
	r.utter(t, r.line("turn off the lights", alan), 4*chunkBytes)
	r.settled(t)
	if r.l.Session() != nil {
		t.Error("speech after the window closed opened a session")
	}
}

// Stage three: the voice must be a household member's. The television
// activating the wake model gets no session and a journalled negative.
//
// verifies SPEC §9.3
func TestAnUnknownVoiceCannotConfirmAWake(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("and now the weather", stranger), 4*chunkBytes)

	rej := r.awaitKind(t, listen.DeviceConversation("kitchen"), journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "unknown_speaker" {
		t.Errorf("reason = %q, want unknown_speaker", rej.Fields["reason"])
	}
	if r.l.Session() != nil {
		t.Error("an unknown voice opened a session")
	}
}

// Nobody enrolled is the fresh install: stage three cannot judge, so it does
// not, and the session opens for a guest (SPEC §5).
//
// verifies SPEC §5
func TestWithNobodyEnrolledTheWakeOpensAGuestSession(t *testing.T) {
	r := newRig(t, silent(), func(c *listen.Config) { c.Speakers = nil })

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("what time is it", stranger), 4*chunkBytes)

	s := r.session(t)
	opened := r.awaitKind(t, s.ConversationID(), journal.KindSessionOpened, 1)
	if opened.Fields["speaker_id"] != "" {
		t.Errorf("speaker_id = %q, want a guest", opened.Fields["speaker_id"])
	}
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 1)
	if _, has := heard.Fields["embedding_json"]; has {
		t.Error("no resolver, yet an embedding was recorded")
	}
}

// Speech while the session is speaking is offered to the gate on every
// partial, carrying the DAC's own position; when it passes, speech stops and
// the correction is heard next.
//
// verifies SPEC §4.3, §3.2.1
func TestSpeechDuringPlaybackStopsItAtTheDACPosition(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	// One second of audio reaches the DAC. Sent by the test, not a synthesiser:
	// the position is the device's report, whoever fed it.
	if err := r.link.SendTTS(quiet(2 * 16000)); err != nil {
		t.Fatal(err)
	}
	r.dev.AwaitTTS(t, 2*16000)
	r.dev.Play(t, 16000)

	r.speak(t, r.line("no the other one", alan), 2*rigPartials)

	conv := s.ConversationID()
	cut := r.awaitKind(t, conv, journal.KindBargeInDetected, 1)
	if cut.Fields["tts_position_ms"] != "1000" {
		t.Errorf("tts_position_ms = %q, want 1000", cut.Fields["tts_position_ms"])
	}
	audio, ok := r.blobs.Bytes(cut.AudioRef)
	if !ok || len(audio) == 0 {
		t.Errorf("the candidate's audio %q is not stored", cut.AudioRef)
	}
	r.awaitKind(t, conv, journal.KindSpeechTruncated, 1)

	r.pause(t, rigSilence)
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "no the other one" {
		t.Errorf("the correction was heard as %q", heard.Fields["text"])
	}
	if got := r.count(t, conv, journal.KindBargeInDetected); got != 1 {
		t.Errorf("%d barge-ins detected for one interruption", got)
	}
}

// PLAYED counts the whole connection, so after any earlier speech the raw
// position is not how far into this answer the person interrupted. The cut
// has to share its origin with speech_truncated's frames_played, or the pair
// the cut produces cannot be reproduced from the record (SPEC §8).
//
// verifies SPEC §3.2.1, §8
func TestTheCutIsMeasuredFromTheSpeechItInterrupts(t *testing.T) {
	play := &fakePlayback{}
	r := newRig(t, talking(), func(c *listen.Config) { c.Playback = play })
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	// Two seconds of audio, in bytes: two frames each.
	if err := r.link.SendTTS(quiet(2 * 2 * 16000)); err != nil {
		t.Fatal(err)
	}
	r.dev.AwaitTTS(t, 2*2*16000)
	// A second of the connection is behind the DAC before the speech that gets
	// interrupted begins, and a second of that speech is heard.
	r.dev.Play(t, 16000)
	play.rebase(16000)
	r.dev.Play(t, 16000)

	r.speak(t, r.line("no the other one", alan), 2*rigPartials)

	cut := r.awaitKind(t, s.ConversationID(), journal.KindBargeInDetected, 1)
	if cut.Fields["tts_position_ms"] != "1000" {
		t.Errorf("tts_position_ms = %q, want 1000: the offset into the speech cut, not into the connection",
			cut.Fields["tts_position_ms"])
	}
}

// The television during playback: loud, wordy, and nobody's. The gate rejects
// it at speaker identity and journals the rejection; playback continues.
//
// verifies SPEC §4.3
func TestATelevisionDuringPlaybackIsRejectedAtSpeakerID(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	r.speak(t, r.line("and now the weather", stranger), 2*rigPartials)

	conv := s.ConversationID()
	rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 1)
	if rej.Fields["stage"] != "speaker_id" {
		t.Errorf("stage = %q, want speaker_id", rej.Fields["stage"])
	}
	if _, ok := r.blobs.Bytes(rej.AudioRef); !ok {
		t.Errorf("the rejected candidate's audio %q is not stored", rej.AudioRef)
	}
	r.settled(t)
	if got := r.count(t, conv, journal.KindBargeInDetected); got != 0 {
		t.Errorf("the television stopped speech: %d detected", got)
	}
	if !slices.Contains(s.Children(), "speaking") {
		t.Error("speech did not continue past the rejection")
	}
}

// Speech while nothing is playing is not a candidate at all: a partial with
// no Speaking child alive is never offered to the gate.
//
// verifies SPEC §4.3
func TestSpeechWhileNothingPlaysIsNotACandidate(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*rigPartials)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	for _, k := range []journal.Kind{journal.KindBargeInDetected, journal.KindBargeInRejected} {
		if got := r.count(t, conv, k); got != 0 {
			t.Errorf("%d %s with nothing playing", got, k)
		}
	}
}

// Speech past the utterance bound is decoded as it stands and the rest is a
// new utterance. The endpointer saw no boundary -- the person is still
// talking -- so nothing but this will open one, and the tail would otherwise
// be dropped until the next silence.
//
// verifies SPEC §4.5
func TestSpeechPastTheBoundContinuesInANewUtterance(t *testing.T) {
	// Wide enough that the wake's own utterance, its lead-in and the silence
	// that ends it all fit: only the long request below is meant to overrun.
	bound := 12 * chunkBytes
	r := newRig(t, silent(), func(c *listen.Config) { c.STT.MaxBytes = bound })

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)

	// One breath, longer than the bound: the opening fills it and the tail
	// follows with no quiet in between.
	r.speak(t, r.line("turn off every light in the house and the", alan), bound)
	r.utter(t, r.line("one in the garage too", alan), 3*chunkBytes)

	tail := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 3)
	if tail.Fields["text"] != "one in the garage too" {
		t.Errorf("the tail of the request was heard as %q", tail.Fields["text"])
	}
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "turn off every light in the house and the" {
		t.Errorf("what filled the bound was heard as %q", heard.Fields["text"])
	}
}

// Hardware mute is authoritative (CONTRIBUTING §7): audio under it is dropped
// on the floor, an utterance it lands on ends with no transcript, and nothing
// is buffered through it.
//
// verifies SPEC §3.2
func TestMuteDropsEverything(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	decodes, embeds, blobs := r.stt.decodes(), r.emb.embeds(), len(r.blobs.Refs())

	// Mid-utterance: speech has started when the switch is thrown. One chunk,
	// short of a partial, so no decode is in flight to race the count below.
	secret := r.line("the secret is", alan)
	r.speak(t, secret, chunkBytes)
	r.dev.SendMute(t, bridge.Mute{Hardware: true})
	r.speak(t, secret, 8*chunkBytes)
	r.pause(t, 2*rigSilence)
	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, secret, 8*chunkBytes)
	r.pause(t, 2*rigSilence)
	r.dev.SendMute(t, bridge.Mute{})
	r.settled(t)

	if got := r.count(t, conv, journal.KindUtteranceTranscribed); got != 1 {
		t.Errorf("%d transcripts; speech under mute was heard", got)
	}
	if got := r.stt.decodes(); got != decodes {
		t.Errorf("%d decodes under mute", got-decodes)
	}
	if got := r.emb.embeds(); got != embeds {
		t.Errorf("%d embeds under mute", got-embeds)
	}
	if got := len(r.blobs.Refs()); got != blobs {
		t.Errorf("%d blobs stored under mute", got-blobs)
	}

	// Unmuted, the same session hears again.
	lights := r.line("turn off the lights", alan)
	r.utter(t, lights, 4*chunkBytes)
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "turn off the lights" {
		t.Errorf("after unmute heard %q", heard.Fields["text"])
	}
	// Exactly the unmuted speech: nothing from under the mute rode along.
	audio, _ := r.blobs.Bytes(heard.AudioRef)
	if got := voiced(audio, lights); got != 4*chunkBytes {
		t.Errorf("the next utterance holds %d bytes of its voice, want %d", got, 4*chunkBytes)
	}
	if got := voiced(audio, secret); got != 0 {
		t.Errorf("%d bytes from under the mute were buffered into the next utterance", got)
	}
}

// voiced counts the bytes of audio at exactly this amplitude.
func voiced(pcm []byte, amplitude int16) int {
	n := 0
	for i := 0; i+1 < len(pcm); i += 2 {
		if int16(binary.LittleEndian.Uint16(pcm[i:])) == amplitude {
			n += 2
		}
	}
	return n
}

// Without a wake word there is no session and no listening: the mic streams
// anyway, and the host must not transcribe or keep any of it.
//
// verifies SPEC §4.5
func TestAudioWithNoWakeIsDropped(t *testing.T) {
	r := newRig(t, silent())
	r.utter(t, r.line("find zeppelin", alan), 8*chunkBytes)
	r.settled(t)
	if r.stt.decodes() != 0 || r.emb.embeds() != 0 || len(r.blobs.Refs()) != 0 {
		t.Errorf("idle audio was processed: %d decodes, %d embeds, %d blobs",
			r.stt.decodes(), r.emb.embeds(), len(r.blobs.Refs()))
	}
	if r.l.Session() != nil {
		t.Error("audio with no wake opened a session")
	}
}

// The link's lifetime owns the listener. Cancelling it mid-decode cancels the
// decode, exits every goroutine, and ends the session the device can no
// longer carry.
//
// verifies SPEC §4
func TestCancelExitsEveryGoroutine(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)

	resume := r.stt.park()
	defer resume()
	r.speak(t, r.line("and the bedroom", alan), 2*rigPartials)
	decode := r.stt.parked(t)

	r.cancel()
	select {
	case <-decode.Done():
	case <-time.After(patience):
		t.Fatal("cancelling the listener did not cancel the decode in flight")
	}
	select {
	case <-r.l.Done():
	case <-time.After(patience):
		t.Fatal("the listener did not finish on cancel")
	}
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("the session outlived its device")
	}
	closed := r.awaitKind(t, conv, journal.KindSessionClosed, 1)
	if closed.Fields["reason"] != "device_lost" {
		t.Errorf("close reason = %q, want device_lost", closed.Fields["reason"])
	}
}

// Utterances are heard in the order they were spoken, one turn at a time,
// even when the second ends while the first's turn is still running.
//
// verifies SPEC §4
func TestUtterancesAreHeardInOrder(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	r.session(t)
	r.speaker.wrote(t)

	// Each, with its trailing silence, shorter than a partial: neither reaches
	// the gate, so they queue.
	r.utter(t, r.line("also", alan), chunkBytes)
	r.utter(t, r.line("and the bedroom", alan), chunkBytes)
	r.settled(t)
	if got := len(r.engine.heard()); got != 1 {
		t.Fatalf("%d turns ran while the first was still speaking", got)
	}

	r.speaker.let()
	await(t, "three turns", func() bool { return len(r.engine.heard()) == 3 })
	var texts []string
	for _, in := range r.engine.heard() {
		texts = append(texts, in.Text)
	}
	if want := []string{"find zeppelin", "also", "and the bedroom"}; !slices.Equal(texts, want) {
		t.Errorf("turns = %q, want %q", texts, want)
	}
}

// A second voice chiming in is attributed per utterance: unknown stays a
// guest inside the same conversation, and the vector is recorded regardless.
//
// verifies SPEC §5
func TestAGuestChimingInIsHeardAsAGuest(t *testing.T) {
	r := newRig(t, silent())
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)

	r.utter(t, r.line("no, make it blue", stranger), 4*chunkBytes)
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["speaker_id"] != "" {
		t.Errorf("a stranger was attributed to %q", heard.Fields["speaker_id"])
	}
	if heard.Fields["embedding_json"] != "[0,0,0,1]" {
		t.Errorf("embedding_json = %q; the guest's vector must still be recorded", heard.Fields["embedding_json"])
	}
}

func TestOpenRequiresItsWiring(t *testing.T) {
	ctx := context.Background()
	if _, err := listen.Open(ctx, listen.Config{}); err == nil {
		t.Error("a listener with no wiring opened")
	}
	if _, err := listen.Open(ctx, listen.Config{Satellite: "kitchen"}); err == nil {
		t.Error("a listener with no sessions opened")
	}
}

// The wait for an answer starts when Alan stops talking, not when the
// endpointer is sure he has: he waits through the quiet it needs too. When
// the kitchen satellite first plays the answer, the turn journals how long
// that was (ADR-0035).
//
// verifies SPEC §11
func TestTheWaitForAnAnswerStartsWhenSpeechStops(t *testing.T) {
	// The journal's clock reads 12:00:00 when the answer starts playing; the
	// listener's read 1.84 s earlier when the endpoint fired, after the rig's
	// 128 ms of quiet (four 32 ms chunks).
	endpoint := time.Date(2026, 10, 6, 11, 59, 58, 160_000_000, time.UTC)
	r := newRig(t, talking(), func(c *listen.Config) { c.Clock = journal.FixedClock(endpoint) })
	r.speaker.dac = true
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, lights, 8*chunkBytes)

	conv := r.session(t).ConversationID()
	start := r.awaitKind(t, conv, journal.KindSpeechStarted, 1)
	if start.Fields["wait_ms"] != "1968" || start.Fields["call_id"] != "s1" {
		t.Errorf("speech_started = %v, want call s1 after 1840 + 128 ms", start.Fields)
	}
}

// Without a clock the ask has no stop time, and the start says so by leaving
// the wait out rather than measuring from when the transcript landed.
//
// verifies SPEC §11
func TestAListenerWithoutAClockRecordsNoWait(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.dac = true
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, lights, 8*chunkBytes)

	conv := r.session(t).ConversationID()
	start := r.awaitKind(t, conv, journal.KindSpeechStarted, 1)
	if wait, ok := start.Fields["wait_ms"]; ok {
		t.Errorf("wait_ms = %q with no clock to measure it", wait)
	}
}

// With Smart Turn deciding, Alan waits through the short pause the judge
// needed, not the 800 ms of quiet Energy would have: the kitchen satellite's
// answer starts 1.84 s after the endpoint and 256 ms after he stopped.
//
// verifies SPEC §4.5, §11
func TestASemanticEndpointShortensTheWait(t *testing.T) {
	endpoint := time.Date(2026, 10, 6, 11, 59, 58, 160_000_000, time.UTC)
	j := &judge{answers: []judgement{{done: true}}}
	ep, q := semantic(j, nil)
	ep.Threshold = 0.05
	r := newRig(t, talking(), func(c *listen.Config) {
		c.Clock = journal.FixedClock(endpoint)
		c.Endpointer = ep
	})
	r.speaker.dac = true
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, lights, 8*chunkBytes)
	r.pause(t, pauseChunks*chunkBytes)
	r.settled(t)
	q.run()
	r.pause(t, chunkBytes)

	conv := r.session(t).ConversationID()
	start := r.awaitKind(t, conv, journal.KindSpeechStarted, 1)
	if start.Fields["wait_ms"] != "2096" {
		t.Errorf("wait_ms = %q, want 1840 + the 256 ms pause", start.Fields["wait_ms"])
	}
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	if heard.Fields["text"] != "turn off the kitchen lights" {
		t.Errorf("heard %q", heard.Fields["text"])
	}
	if got := len(j.asked[0]); got != 8*chunkBytes+pauseChunks*chunkBytes {
		t.Errorf("the judge was sent %d bytes, want the turn and its pause", got)
	}
}

// stalled is a judge that never answers: it holds its ask until the context
// ends, and says when it has let go.
type stalled struct {
	asked chan struct{}
	gone  atomic.Bool
}

func (s *stalled) Complete(ctx context.Context, _ []byte) (bool, error) {
	close(s.asked)
	<-ctx.Done()
	s.gone.Store(true)
	return false, ctx.Err()
}

// An ask in flight when the kitchen satellite drops is cancelled with the
// link, and the listener is not done until the ask has returned (CONTRIBUTING
// §6).
//
// verifies SPEC §4.5
func TestAnAskInFlightEndsWithTheLink(t *testing.T) {
	j := &stalled{asked: make(chan struct{})}
	ep := listen.NewSemantic(j)
	ep.Threshold = 0.05
	r := newRig(t, talking(), func(c *listen.Config) { c.Endpointer = ep })
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, lights, 8*chunkBytes)
	r.pause(t, pauseChunks*chunkBytes)
	select {
	case <-j.asked:
	case <-time.After(patience):
		t.Fatal("the judge was never asked")
	}

	r.cancel()
	select {
	case <-r.l.Done():
	case <-time.After(patience):
		t.Fatal("the listener outlived its context")
	}
	if !j.gone.Load() {
		t.Error("the listener finished before the ask it started")
	}
}

// Alan says "set a timer for", thinks for a second, and says "twelve
// minutes". Smart Turn hears the falling pitch as finished; the words, from
// the same transcriber the turn is decoded with, hold it, and the kitchen
// satellite hears one command rather than a timer with no duration.
//
// verifies SPEC §4.5
func TestACutOffCommandIsHeardWholeAfterItsPause(t *testing.T) {
	j := &judge{answers: []judgement{{done: true}, {done: true}}}
	ep, q := semantic(j, nil)
	ep.Threshold = 0.05
	r := newRig(t, talking(), func(c *listen.Config) {
		ep.Judge = listen.Dangling{Judge: j, Words: c.Transcriber}
		c.Endpointer = ep
	})
	r.speaker.dac = true
	cut := r.line("set a timer for", alan)
	whole := r.line("set a timer for twelve minutes", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, cut, 8*chunkBytes)
	r.pause(t, pauseChunks*chunkBytes)
	r.settled(t)
	q.run()
	r.pause(t, ms(1000))
	r.speak(t, whole, 8*chunkBytes)
	r.pause(t, pauseChunks*chunkBytes)
	r.settled(t)
	q.run()
	r.pause(t, chunkBytes)

	conv := r.session(t).ConversationID()
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	if heard.Fields["text"] != "set a timer for twelve minutes" {
		t.Errorf("heard %q, want the whole command", heard.Fields["text"])
	}
	r.awaitKind(t, conv, journal.KindSpeechStarted, 1)
	if n := r.count(t, conv, journal.KindUtteranceTranscribed); n != 1 {
		t.Errorf("%d utterances transcribed, want the one command", n)
	}
	if len(j.asked) != 2 {
		t.Errorf("Smart Turn judged %d pauses, want both", len(j.asked))
	}
}
