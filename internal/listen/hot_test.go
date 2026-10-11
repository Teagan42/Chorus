package listen_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Alan says one word, "stop", over the kitchen's answer. It is one word,
// and it stops speech; once he pauses it is heard as a stop, and the model
// is not asked what to say to it.
//
// verifies SPEC §4.3
func TestAOneWordStopOverAnAnswerStopsItAndIsNotAnswered(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	r.speak(t, r.line("stop", alan), 2*rigPartials)
	conv := s.ConversationID()
	if det := r.awaitKind(t, conv, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "stop" {
		t.Errorf("barge_in_detected = %v, want the stop", det.Fields)
	}
	r.awaitKind(t, conv, journal.KindSpeechTruncated, 1)
	r.pause(t, rigSilence)

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "stop" || heard.Fields["hot_word"] != "stop" {
		t.Errorf("utterance_transcribed = %v, want a stop", heard.Fields)
	}
	if n := len(r.engine.heard()); n != 1 {
		t.Errorf("the model was asked %d times, want only find zeppelin", n)
	}
}

// The oven timer goes off in the kitchen, in a session nobody may answer.
// Alan says "stop" over it, and it stops where the kitchen had got to, the
// session closes as said, and nothing he said becomes a turn.
//
// verifies SPEC §4.2, §4.3
func TestAStopOverATimerSilencesIt(t *testing.T) {
	r := newRig(t, silent())
	r.speaker.hold = true
	conv, err := r.l.Announce(context.Background(), ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	r.speaker.wrote(t)

	r.speak(t, r.line("stop", alan), 2*rigPartials)
	if det := r.awaitKind(t, conv, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "stop" {
		t.Errorf("barge_in_detected = %v, want the stop", det.Fields)
	}
	cut := r.awaitKind(t, conv, journal.KindSpeechTruncated, 1)
	if cut.Fields["reason"] != "barge_in" || !strings.HasPrefix(ovenDone.Text, cut.Fields["spoken_text"]) {
		t.Errorf("speech_truncated = %v, want the timer cut by Alan", cut.Fields)
	}
	if closed := r.awaitKind(t, conv, journal.KindSessionClosed, 1); closed.Fields["reason"] != "announced" {
		t.Errorf("closed as %q, want announced", closed.Fields["reason"])
	}
	r.pause(t, rigSilence)
	r.settled(t)
	if n := r.count(t, conv, journal.KindUtteranceTranscribed); n != 0 {
		t.Errorf("%d utterances heard by a timer going off", n)
	}
}

// The television says "stop" over the oven timer. It is nobody in the
// household, so the timer is said to the end, and the refusal is kept.
// Alan thanking it is no hot phrase, and is not offered at all.
//
// verifies SPEC §4.3
func TestOnlyAHouseholdStopSilencesATimer(t *testing.T) {
	r := newRig(t, silent())
	r.speaker.hold = true
	conv, err := r.l.Announce(context.Background(), ovenDone)
	if err != nil {
		t.Fatal(err)
	}
	r.speaker.wrote(t)

	r.utter(t, r.line("stop", stranger), 2*rigPartials)
	rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 1)
	if rej.Fields["stage"] != "speaker_id" || rej.Fields["hot_word"] != "stop" {
		t.Errorf("barge_in_rejected = %v, want the television's stop at speaker_id", rej.Fields)
	}
	r.utter(t, r.line("that's great thanks", alan), 2*rigPartials)
	r.settled(t)
	r.speaker.let()

	if spoken := r.awaitKind(t, conv, journal.KindSpeechSpoken, 1); spoken.Fields["text"] != ovenDone.Text {
		t.Errorf("speech_spoken = %v, want the whole timer", spoken.Fields)
	}
	r.awaitKind(t, conv, journal.KindSessionClosed, 1)
	// One per partial the gate judged, every one of them the television's.
	for _, e := range r.events(t, conv) {
		if e.Kind == journal.KindBargeInRejected && e.Fields["hot_word"] != "stop" {
			t.Errorf("barge_in_rejected = %v, want only the television's stop", e.Fields)
		}
	}
	if n := r.count(t, conv, journal.KindBargeInDetected) + r.count(t, conv, journal.KindUtteranceTranscribed); n != 0 {
		t.Errorf("%d detections or utterances over a timer nobody stopped", n)
	}
}

// heldLook is the garage cover read, slow: it answers once released, and
// gives up when its context does.
type heldLook struct{ release chan struct{} }

func (h heldLook) Invoke(ctx context.Context, _ string) (string, error) {
	select {
	case <-h.release:
		return `{"state":"closed"}`, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// lookingAtTheGarage is Alan waiting in silence while the kitchen reads the
// garage door, which it has not finished doing.
func lookingAtTheGarage(t *testing.T) (*rig, *session.Session, heldLook) {
	t.Helper()
	look := heldLook{release: make(chan struct{})}
	r := newRigSession(t, []step{
		{act: session.ToolCall{ID: "c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}, func(c *session.Config) {
		c.Tools = map[string]session.Tool{"ha_get_state": look}
		c.Rounds = 1
	})
	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("is the garage shut", alan), 4*chunkBytes)
	s := r.session(t)
	await(t, "the garage read", func() bool { return slices.Contains(s.Children(), "tool:c1") })
	return r, s, look
}

// Nothing is playing while the kitchen reads the garage, so nothing is a
// candidate, except "never mind": it reaches the working turn, which ends
// as a barge-in ends it, and is not answered.
//
// verifies SPEC §4.3, §4.4
func TestNeverMindReachesATurnWorkingInSilence(t *testing.T) {
	r, s, _ := lookingAtTheGarage(t)
	conv := s.ConversationID()

	r.utter(t, r.line("never mind", alan), 2*rigPartials)
	if det := r.awaitKind(t, conv, journal.KindBargeInDetected, 1); det.Fields["hot_word"] != "never_mind" {
		t.Errorf("barge_in_detected = %v, want never mind", det.Fields)
	}
	if res := r.awaitKind(t, conv, journal.KindToolResult, 1); res.Fields["outcome"] != "cancelled" {
		t.Errorf("tool_result = %v, want the read cancelled", res.Fields)
	}
	if heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2); heard.Fields["hot_word"] != "never_mind" {
		t.Errorf("utterance_transcribed = %v, want never mind", heard.Fields)
	}
	if n := len(r.engine.heard()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
}

// "Stop" stops speech, and nothing is speaking: said while the kitchen
// reads the garage it is no candidate, the read finishes, and the model is
// asked about it as it always was.
//
// verifies SPEC §4.3
func TestAStopWithNothingPlayingIsNoCandidate(t *testing.T) {
	r, s, look := lookingAtTheGarage(t)
	conv := s.ConversationID()

	r.utter(t, r.line("stop", alan), 2*rigPartials)
	r.settled(t)
	close(look.release)

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["hot_word"] != "" {
		t.Errorf("utterance_transcribed = %v, want an ordinary turn", heard.Fields)
	}
	if res := r.awaitKind(t, conv, journal.KindToolResult, 1); res.Fields["outcome"] != "ok" {
		t.Errorf("tool_result = %v, want the read finished", res.Fields)
	}
	if n := r.count(t, conv, journal.KindBargeInDetected) + r.count(t, conv, journal.KindBargeInRejected); n != 0 {
		t.Errorf("%d candidates with nothing playing", n)
	}
	await(t, "the stop's turn", func() bool { return len(r.engine.heard()) == 2 })
}
