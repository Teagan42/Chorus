package listen_test

import (
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// heardTexts is what the conversation's log says was heard, in order.
func (r *rig) heardTexts(t *testing.T, convID string) []string {
	t.Helper()
	var out []string
	for _, e := range r.events(t, convID) {
		if e.Kind == journal.KindUtteranceTranscribed {
			out = append(out, e.Fields["text"])
		}
	}
	return out
}

// The television talks over the kitchen's answer, all the way to a pause.
// The gate refused to let it stop speech, and that refusal stands for the
// whole utterance: it is never heard as a turn, so the model is not asked
// to answer the weather forecast. Alan's next question is heard as usual,
// and since utterances become turns in order, its arrival proves the
// television's never will.
//
// verifies SPEC §4.3, §5
func TestATelevisionThatTalkedOverSpeechNeverBecomesATurn(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	// The pause comes once the gate has ruled: an utterance that ends first
	// is never offered as a candidate at all.
	r.speak(t, r.line("and now the weather for the weekend", stranger), 2*rigPartials)
	if rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 1); rej.Fields["stage"] != "speaker_id" {
		t.Errorf("stage = %q, want speaker_id", rej.Fields["stage"])
	}
	r.pause(t, rigSilence)

	r.speaker.let()
	await(t, "speech to finish", func() bool { return !slices.Contains(s.Children(), "speaking") })
	r.utter(t, r.line("play the second one", alan), 4*chunkBytes)
	r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)

	if got, want := r.heardTexts(t, conv), []string{"find zeppelin", "play the second one"}; !slices.Equal(got, want) {
		t.Errorf("heard %q, want %q: the television is not a turn", got, want)
	}
	await(t, "alan's second turn", func() bool { return len(r.engine.heard()) == 2 })
	for _, in := range r.engine.heard() {
		if in.Text == "and now the weather for the weekend" {
			t.Errorf("the model was asked to answer the television: %+v", in)
		}
	}
	if got := r.count(t, conv, journal.KindBargeInDetected); got != 0 {
		t.Errorf("the television stopped speech: %d detected", got)
	}
}

// Alan says "thanks" over the end of the answer. One word does not stop
// speech, but it is Alan's voice, not the television's, so it is still heard
// as his turn once he pauses.
//
// verifies SPEC §4.3
func TestAHouseholdVoiceThatDidNotStopSpeechIsStillHeard(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.hold = true

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("find zeppelin", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	r.speaker.wrote(t)
	await(t, "the speaking child", func() bool { return slices.Contains(s.Children(), "speaking") })

	// The pause comes once the gate has ruled: an utterance that ends first
	// is never offered as a candidate at all.
	r.speak(t, r.line("thanks", alan), 2*rigPartials)
	if rej := r.awaitKind(t, conv, journal.KindBargeInRejected, 1); rej.Fields["stage"] != "partial_length" {
		t.Errorf("stage = %q, want partial_length", rej.Fields["stage"])
	}
	r.pause(t, rigSilence)
	r.speaker.let()

	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["text"] != "thanks" || heard.Fields["speaker_id"] != "alan" {
		t.Errorf("utterance_transcribed = %v, want Alan's thanks", heard.Fields)
	}
}

// A friend over for dinner chimes in after the kitchen has answered Alan.
// Nothing is playing, so there is no barge-in to gate: the friend is heard,
// as a guest, with how the voice matched in the log, and the model is asked
// as nobody rather than as Alan (SPEC §5).
//
// verifies SPEC §5
func TestAGuestChimingInIsHeardAsAGuestNotAsAlan(t *testing.T) {
	r := newRig(t, silent())

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, r.line("is the lasagne done", alan), 4*chunkBytes)
	s := r.session(t)
	conv := s.ConversationID()
	if heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1); heard.Fields["speaker_match"] != "identified" {
		t.Errorf("Alan's speaker_match = %q, want identified", heard.Fields["speaker_match"])
	}

	r.utter(t, r.line("can i have the recipe", stranger), 4*chunkBytes)
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 2)
	if heard.Fields["speaker_id"] != "" || heard.Fields["speaker_match"] != "below_threshold" {
		t.Errorf("utterance_transcribed = %v, want a voice that matched nobody", heard.Fields)
	}
	await(t, "the guest's turn", func() bool { return len(r.engine.heard()) == 2 })
	if in := r.engine.heard()[1]; in.Speaker != "" {
		t.Errorf("the guest's turn was asked as %q, want a guest", in.Speaker)
	}
}
