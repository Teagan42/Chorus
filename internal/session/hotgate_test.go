package session_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

const weather = "Sunny this morning, clouding over by noon, with rain from three and a low of nine tonight."

// kitchenHousehold is the gate Teagan's kitchen runs: she and Alice are
// enrolled, and two words is the floor for anything that is not a hot phrase.
func kitchenHousehold(cfg *session.Config) {
	cfg.Gate.Household = []string{"teagan", "alice"}
}

// forecasting opens Teagan's session with the long weather answer playing
// and held, so each candidate lands mid-sentence.
func forecasting(t *testing.T, tweak func(*session.Config)) (*rig, *session.Session, <-chan error) {
	t.Helper()
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: weather, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, tweak)
	r.speaker.hold = true
	r.speaker.cut = len("Sunny this morning, ")
	s := r.open(t, "teagan")
	errc := heard(s, "what's the weather today")
	r.speaker.wrote(t)
	return r, s, errc
}

// Teagan, in the kitchen, says "stop" over the long weather answer. One
// word is below the gate's floor, but a hot phrase skips only that stage:
// she is loud enough and she is Teagan, so speech stops, and the detection
// says which phrase let it through.
//
// verifies SPEC §4.3
func TestTeagansOneWordStopIsAdmitted(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/stop", SpeakerID: "teagan", Energy: 0.9, Partial: "Stop.",
	})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v, want Teagan's stop admitted", ok, err)
	}
	wait(t, errc)

	detected := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected)
	if detected.Fields["hot_word"] != "stop" || detected.Fields["tts_position_ms"] != "1300" {
		t.Errorf("barge_in_detected = %v, want hot_word stop at 1300 ms", detected.Fields)
	}
	cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	if cut.Fields["spoken_text"] != "Sunny this morning, " || cut.Fields["reason"] != "barge_in" {
		t.Errorf("speech_truncated = %v, want the forecast cut by the person", cut.Fields)
	}
}

// The television says "stop" over the same answer. It is loud and it is a
// hot phrase, but it is nobody in the household: the speaker stage refuses
// it, the forecast plays on, and the refusal names the phrase for tuning.
//
// verifies SPEC §4.3
func TestTheTelevisionsStopIsRefusedAtTheSpeakerStage(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	for _, who := range []string{"", "stranger"} {
		ok, err := s.BargeIn(t.Context(), session.Candidate{
			PositionMS: 900, AudioRef: "blob://mic/tv", SpeakerID: who, Energy: 0.9, Partial: "Stop!",
		})
		if err != nil || ok {
			t.Fatalf("barge-in by %q: ok=%v err=%v, want it refused", who, ok, err)
		}
	}
	close(r.speaker.release)
	wait(t, errc)

	for _, e := range r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected) {
		if e.Fields["stage"] != "speaker_id" || e.Fields["hot_word"] != "stop" {
			t.Errorf("barge_in_rejected = %v, want speaker_id naming the stop", e.Fields)
		}
	}
	if st := r.state(t, s.ConversationID()); st.Interrupted {
		t.Error("the television's stop stopped the forecast")
	}
}

// A quiet "stop" is still below the energy floor: the phrase skips the
// word count, not the VAD.
//
// verifies SPEC §4.3
func TestAQuietStopIsRefusedAtTheEnergyStage(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{SpeakerID: "teagan", Energy: 0.05, Partial: "stop", AudioRef: "blob://mic/whisper"})
	if err != nil || ok {
		t.Fatalf("barge-in: ok=%v err=%v, want it refused", ok, err)
	}
	close(r.speaker.release)
	wait(t, errc)

	if got := r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected); len(got) != 1 || got[0].Fields["stage"] != "vad" {
		t.Errorf("barge_in_rejected = %v, want one at vad", got)
	}
}

// "Stop the music" is a request that begins with a hot word, not a hot
// phrase: it stops speech as any three words from Teagan do, and records
// no phrase. "Uh" is still a cough.
//
// verifies SPEC §4.3
func TestOnlyTheWholePartialIsAHotPhrase(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{SpeakerID: "teagan", Energy: 0.9, Partial: "uh", AudioRef: "blob://mic/uh"})
	if err != nil || ok {
		t.Fatalf("uh: ok=%v err=%v, want it refused", ok, err)
	}
	ok, err = s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 700, SpeakerID: "teagan", Energy: 0.9, Partial: "stop the music", AudioRef: "blob://mic/music",
	})
	if err != nil || !ok {
		t.Fatalf("stop the music: ok=%v err=%v, want it admitted on its words", ok, err)
	}
	wait(t, errc)

	if got := r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected); len(got) != 1 || got[0].Fields["stage"] != "partial_length" || got[0].Fields["hot_word"] != "" {
		t.Errorf("barge_in_rejected = %v, want uh at partial_length with no phrase", got)
	}
	if detected := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected); detected.Fields["hot_word"] != "" {
		t.Errorf("barge_in_detected = %v, want no hot phrase", detected.Fields)
	}
}

// With nothing identifying speakers the speaker stage is skipped for a
// hot phrase as for anything else (ADR-0031): any loud enough "stop" stops
// the house, and the detection says the stage did not run.
//
// verifies SPEC §4.3
func TestAStopWithoutSpeakerIdentificationGatesOnEnergyAlone(t *testing.T) {
	r, s, errc := forecasting(t, func(cfg *session.Config) {
		kitchenHousehold(cfg)
		cfg.Gate.SpeakerIDUnavailable = true
	})

	ok, err := s.BargeIn(t.Context(), session.Candidate{PositionMS: 500, Energy: 0.9, Partial: "stop", AudioRef: "blob://mic/stop"})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v, want it admitted", ok, err)
	}
	wait(t, errc)

	detected := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected)
	if detected.Fields["hot_word"] != "stop" || detected.Fields["speaker_stage_skipped"] != "true" {
		t.Errorf("barge_in_detected = %v, want a stop with the speaker stage skipped", detected.Fields)
	}
}

// ofKind lists every event of a kind, in order.
func (r *rig) ofKind(t *testing.T, convID string, k journal.Kind) []journal.Event {
	t.Helper()
	var out []journal.Event
	for _, e := range r.events(t, convID) {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}
