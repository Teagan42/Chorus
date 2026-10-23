package listen_test

import (
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/session"
)

// rigPause is the quiet the rig's endpointer calls a pause: half the quiet
// that ends an utterance, as 200 ms is a quarter of the default 800.
const rigPause = 2 * chunkBytes

// speculating is the rig with pauses guessed at, and Alan already one turn
// into a kitchen conversation, its session at rest.
func speculating(t *testing.T) (*rig, *session.Session) {
	t.Helper()
	r := newRig(t, silent(), func(c *listen.Config) {
		c.Endpointer = &listen.Energy{Threshold: 0.05, Silence: rigSilence, Pause: rigPause}
		c.Speculate = true
	})
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("is the kitchen light on", alan), 6*chunkBytes)
	s := r.session(t)
	await(t, "the first turn to finish", func() bool {
		return len(r.engine.heard()) == 1 && slices.Equal(s.Children(), []string{"listening"})
	})
	return r, s
}

// Alan says "turn it off" and stops. The pause is decoded and answered
// before the endpointer ends the utterance; when it does, the turn is that
// answer, and neither the ear nor the model is asked again.
//
// verifies SPEC §4.5, §11
func TestAPauseIsAnsweredBeforeTheUtteranceEnds(t *testing.T) {
	r, s := speculating(t)
	off := r.line("turn it off", alan)
	decodes := r.stt.decodes()

	// Short of rigPartials, so the only decodes are the pause's and the end's.
	r.speak(t, off, 2*chunkBytes)
	r.pause(t, rigPause)
	await(t, "the pause to be answered", func() bool { return len(r.engine.heard()) == 2 })
	if n := r.count(t, s.ConversationID(), journal.KindUtteranceTranscribed); n != 1 {
		t.Fatalf("%d utterances heard before the end, want only the first", n)
	}
	if in := r.engine.heard()[1]; in.Text != "turn it off" || in.Speaker != "alan" {
		t.Errorf("the pause asked %q for %q, want turn it off for alan", in.Text, in.Speaker)
	}

	r.pause(t, rigSilence-rigPause)
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "turn it off" || heard.Fields["speaker_id"] != "alan" {
		t.Errorf("utterance_transcribed = %v, want turn it off by alan", heard.Fields)
	}
	done := r.awaitKind(t, s.ConversationID(), journal.KindModelCompleted, 2)
	if done.Fields["asked_early_ms"] == "" {
		t.Error("the kept answer's completion does not say it was asked early")
	}
	if n := len(r.engine.heard()); n != 2 {
		t.Errorf("model asked %d times, want 2: the pause's answer was not kept", n)
	}
	if n := r.stt.decodes() - decodes; n != 1 {
		t.Errorf("%d decodes for the utterance, want the pause's only", n)
	}
	if n := r.count(t, s.ConversationID(), journal.KindSpeculationDiscarded); n != 0 {
		t.Errorf("%d discarded speculations, want none", n)
	}
}

// "Turn off the kitchen lights" -- a breath -- "and the hallway". The guess
// made in the breath is discarded as resumed, and the whole sentence is the
// turn.
//
// verifies SPEC §4.5, §11
func TestSpeechAfterAPauseDiscardsItsGuess(t *testing.T) {
	r, s := speculating(t)
	kitchen := r.line("turn off the kitchen lights", alan)
	both := r.line("turn off the kitchen lights and the hallway", alan)

	r.speak(t, kitchen, 6*chunkBytes)
	r.pause(t, rigPause)
	await(t, "the pause to be answered", func() bool { return len(r.engine.heard()) == 2 })
	r.speak(t, both, 4*chunkBytes)
	r.pause(t, rigSilence)

	d := r.awaitKind(t, s.ConversationID(), journal.KindSpeculationDiscarded, 1)
	if !d.Speculative || d.Fields["reason"] != "resumed" || d.Fields["text"] != "turn off the kitchen lights" {
		t.Errorf("discard = %v (speculative %v), want the kitchen guess, resumed", d.Fields, d.Speculative)
	}
	heard := r.awaitKind(t, s.ConversationID(), journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "turn off the kitchen lights and the hallway" {
		t.Errorf("heard %q, want the whole sentence", heard.Fields["text"])
	}
	await(t, "the sentence to be asked", func() bool { return len(r.engine.heard()) == 3 })
	if in := r.engine.heard()[2]; in.Text != "turn off the kitchen lights and the hallway" {
		t.Errorf("the turn asked %q", in.Text)
	}
	st, err := s.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"is the kitchen light on", "turn off the kitchen lights and the hallway"}; !slices.Equal(st.Heard, want) {
		t.Errorf("replay heard %v, want %v", st.Heard, want)
	}
}

// Alan mutes the satellite mid-pause. The utterance is never heard, so its
// guess is not a turn either.
//
// verifies SPEC §11, CONTRIBUTING §7
func TestAMuteInThePauseDiscardsItsGuess(t *testing.T) {
	r, s := speculating(t)
	r.speak(t, r.line("what's on the calendar tomorrow", alan), 6*chunkBytes)
	r.pause(t, rigPause)
	await(t, "the pause to be answered", func() bool { return len(r.engine.heard()) == 2 })
	r.dev.SendMute(t, bridge.Mute{Hardware: true})

	d := r.awaitKind(t, s.ConversationID(), journal.KindSpeculationDiscarded, 1)
	if d.Fields["reason"] != "not_a_turn" {
		t.Errorf("reason = %q, want not_a_turn", d.Fields["reason"])
	}
	if n := r.count(t, s.ConversationID(), journal.KindUtteranceTranscribed); n != 1 {
		t.Errorf("%d utterances heard, want only the first", n)
	}
}

// Without speculation a pause is only a pause: nothing is decoded or asked
// until the endpointer ends the utterance.
//
// verifies SPEC §4.5
func TestAPauseIsNotGuessedAtUnlessAsked(t *testing.T) {
	r := newRig(t, silent(), func(c *listen.Config) {
		c.Endpointer = &listen.Energy{Threshold: 0.05, Silence: rigSilence, Pause: rigPause}
	})
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("is the kitchen light on", alan), 6*chunkBytes)
	s := r.session(t)
	await(t, "the first turn", func() bool { return len(r.engine.heard()) == 1 })
	decodes := r.stt.decodes()

	r.speak(t, r.line("turn it off", alan), 2*chunkBytes)
	r.pause(t, rigPause)
	r.settled(t)
	if n := r.stt.decodes() - decodes; n != 0 {
		t.Errorf("%d decodes in the pause, want none", n)
	}
	r.pause(t, rigSilence-rigPause)
	r.awaitKind(t, s.ConversationID(), journal.KindModelCompleted, 2)
	if done := r.awaitKind(t, s.ConversationID(), journal.KindModelCompleted, 2); done.Fields["asked_early_ms"] != "" {
		t.Error("a turn asked at its end claims an early ask")
	}
}
