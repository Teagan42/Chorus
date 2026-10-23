package listen_test

import (
	"slices"
	"strconv"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/echo"
	"github.com/teagan42/chorus/internal/echo/echotest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

const playlist = "Playing Late Night Jazz from Spotify, starting with Take Five."

// echoSilence is how long a pause ends an utterance in these tests: the
// built-in 800 ms, so the echo of one answer is one utterance as it is on a
// real satellite, not one per gap between its words.
const echoSilence = 25 * chunkBytes

// livingRoom is Alan's evening: he asked for jazz, and the living-room
// satellite is playing the playlist answer -- held, so every candidate lands
// mid-sentence. Its echo stage compares against ref, which holds what the DAC
// was sent, as the satellite's Speaking side would have placed it.
func livingRoom(t *testing.T, gate func(*session.Gate), tweak ...func(*listen.Config)) (*rig, *session.Session, *echo.Reference, []byte) {
	t.Helper()
	ref := echo.NewReference(0)
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: playlist, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	cfg := append([]func(*listen.Config){func(c *listen.Config) {
		c.Reference = ref
		c.Endpointer = &listen.Energy{Threshold: listen.DefaultSpeechEnergy, Silence: echoSilence}
		// The production cadence and lead-in, not the rig's quick ones: the
		// first partial then holds enough voice for the stage to judge,
		// which is what decides the first candidate (ADR-0096).
		c.STT.PartialEvery = stt.DefaultPartialEvery
		c.LeadIn = listen.DefaultLeadIn
	}}, tweak...)
	r := newRigSession(t, steps, func(c *session.Config) {
		// The satellite's own speech level, as chorusd builds the gate
		// (ADR-0088), and Alan's household.
		c.Gate = session.Gate{MinEnergy: listen.DefaultSpeechEnergy, MinWords: 2, Household: []string{"alan"}}
		if gate != nil {
			gate(&c.Gate)
		}
	}, cfg...)
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, r.line("and put on some jazz", alan), 4*chunkBytes)
	r.pause(t, echoSilence)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	played := echotest.Clip(t, "tts/jazz-playlist")
	if err := r.link.SendTTS(played); err != nil {
		t.Fatal(err)
	}
	r.dev.AwaitTTS(t, len(played))
	ref.Write(0, played)
	return r, s, ref, played
}

// stream sends uplink as the firmware does: each loop publishes PLAYED, then
// pumps the mic, so every chunk arrives with the DAC's position at its end.
//
// Paced on the gate: the next chunk goes once every partial decoded so far
// has been judged, as a real mic's 32 ms a chunk allows. Sent all at once,
// the decodes coalesce into whichever partial is newest and the gate sees
// one candidate for three seconds of audio.
func (r *rig) stream(t *testing.T, conv string, uplink []byte) {
	t.Helper()
	before := r.stt.decodes()
	judged := func() int {
		return r.count(t, conv, journal.KindBargeInRejected) + r.count(t, conv, journal.KindBargeInDetected)
	}
	for off := 0; off < len(uplink); off += chunkBytes {
		chunk := uplink[off:min(off+chunkBytes, len(uplink))]
		r.dev.Play(t, uint64(len(chunk)/2))
		r.dev.SendMic(t, bridge.ChannelAEC, chunk)
		r.settled(t)
		// One interruption per utterance: after it, partials are not offered.
		await(t, "the gate to judge every partial", func() bool {
			return r.count(t, conv, journal.KindBargeInDetected) > 0 || judged() >= r.stt.decodes()-before
		})
	}
}

// The playlist comes back through the living room's microphone and the ear
// reads words in it. It is the device's own voice: every candidate the echo
// stage could judge is refused there, with its score, the answer plays on,
// and none of them cost an embedding -- most candidates while the device
// speaks are its own voice, and ADR-0088 measured each one costing a
// transcription and an embedding before the speaker stage threw it away.
//
// verifies SPEC §4.3
func TestThePlaylistsOwnEchoIsRefusedWithoutAnEmbedding(t *testing.T) {
	r, s, _, played := livingRoom(t, nil)
	r.stt.mu.Lock()
	r.stt.otherwise = "playing late night jazz"
	r.stt.mu.Unlock()

	conv := s.ConversationID()
	r.stream(t, conv, echotest.Kitchen.Echo(played)[:2*3*echotest.Rate])
	var echoes []journal.Event
	await(t, "three candidates refused as the device's own voice", func() bool {
		echoes = echoes[:0]
		for _, e := range r.events(t, conv) {
			if e.Kind == journal.KindBargeInRejected && e.Fields["stage"] == "echo" {
				echoes = append(echoes, e)
			}
		}
		return len(echoes) >= 3
	})
	if got := r.count(t, conv, journal.KindBargeInDetected); got != 0 {
		t.Errorf("the device's own voice stopped it: %d detected", got)
	}
	if !slices.Contains(s.Children(), "speaking") {
		t.Error("the playlist did not play on")
	}
	r.emb.mu.Lock()
	sizes := slices.Clone(r.emb.sizes)
	r.emb.mu.Unlock()
	for _, e := range echoes {
		score, _ := strconv.ParseFloat(e.Fields["echo_score"], 64)
		lag, _ := strconv.Atoi(e.Fields["echo_lag_ms"])
		if score < echo.DefaultThreshold || lag < 120 || lag > 160 {
			t.Errorf("refused at %v, %d ms: want a score over the threshold near the room's 140 ms", score, lag)
		}
		frames, _ := strconv.Atoi(e.Fields["audio_frames"])
		if slices.Contains(sizes, 2*frames) {
			t.Errorf("the candidate at %d frames was embedded before it was refused as an echo", frames)
		}
	}
}

// With nothing identifying speakers (ADR-0031) the speaker stage is
// skipped, so before the echo stage the playlist's own echo, read as four
// words, stopped the playlist. Now it does not.
//
// verifies SPEC §4.3
func TestWithoutAnIdentifierThePlaylistDoesNotStopItself(t *testing.T) {
	r, s, _, played := livingRoom(t, func(g *session.Gate) { g.SpeakerIDUnavailable = true })
	r.stt.mu.Lock()
	r.stt.otherwise = "playing late night jazz"
	r.stt.mu.Unlock()

	conv := s.ConversationID()
	r.stream(t, conv, echotest.Kitchen.Echo(played)[:2*3*echotest.Rate])
	r.awaitKind(t, conv, journal.KindBargeInRejected, 3)
	for _, e := range r.events(t, conv) {
		if e.Kind == journal.KindBargeInDetected {
			t.Errorf("the playlist stopped itself: %v", e.Fields)
		}
	}
}

// Alan says "no, wait" over the playlist, 1.1 s in, where voice.json has
// him cut it, with the device's voice still leaking behind him. Until he
// speaks every candidate is the echo; once he does, the window is his, the
// echo stage passes him, he is Alan, and the cut lands where he spoke.
//
// verifies SPEC §4.3, §4.4
func TestAlanTalkingOverTheEchoStillStopsIt(t *testing.T) {
	r, s, _, played := livingRoom(t, nil)
	r.stt.mu.Lock()
	r.stt.otherwise = "no wait"
	r.stt.mu.Unlock()

	const at = 17600
	alanSays := echotest.Clip(t, "mic/jazz-bargein")
	uplink := echotest.Kitchen.Over(played, alanSays, at)
	conv := s.ConversationID()
	r.stream(t, conv, uplink[:2*at])
	if got := r.count(t, conv, journal.KindBargeInDetected); got != 0 {
		t.Fatalf("the echo before Alan spoke stopped it: %d detected", got)
	}
	// The embedder hears Alan from here on.
	r.emb.mu.Lock()
	r.emb.otherwise = alan
	r.emb.mu.Unlock()
	r.stream(t, conv, uplink[2*at:2*(at+len(alanSays)/2)])

	cut := r.awaitKind(t, conv, journal.KindBargeInDetected, 1)
	score, err := strconv.ParseFloat(cut.Fields["echo_score"], 64)
	if err != nil || score >= echo.DefaultThreshold {
		t.Errorf("Alan's barge-in scored %q, want a score under the threshold", cut.Fields["echo_score"])
	}
	if ms, _ := strconv.Atoi(cut.Fields["tts_position_ms"]); ms < 1100 {
		t.Errorf("cut at %d ms, want after Alan started at 1100", ms)
	}
	r.awaitKind(t, conv, journal.KindSpeechTruncated, 1)
}

// A satellite with no reference wired judges nothing: no score is kept and
// the gate is what it was before the stage existed -- the echo is refused
// at the speaker stage, at the cost of an embedding each.
//
// verifies SPEC §4.3
func TestWithNoReferenceTheSpeakerStageIsTheOnlyAnswer(t *testing.T) {
	r, s, _, played := livingRoom(t, nil, func(c *listen.Config) { c.Reference = nil })
	r.stt.mu.Lock()
	r.stt.otherwise = "playing late night jazz"
	r.stt.mu.Unlock()

	conv := s.ConversationID()
	r.stream(t, conv, echotest.Kitchen.Echo(played)[:2*2*echotest.Rate])
	rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 2)
	if rej.Fields["stage"] != "speaker_id" {
		t.Errorf("stage = %q, want speaker_id", rej.Fields["stage"])
	}
	if _, scored := rej.Fields["echo_score"]; scored {
		t.Errorf("a satellite with no reference kept a score: %v", rej.Fields)
	}
}
