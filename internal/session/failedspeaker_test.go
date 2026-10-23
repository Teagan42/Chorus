package session_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Teagan says "hang on, stop" over the forecast while the speaker-ID
// sidecar is failing. Only a recognised voice stops speech, so the forecast
// plays on -- but the refusal says the embedder failed, not that a stranger
// spoke: an outage is not the television in the tuning corpus (ADR-0097).
//
// verifies SPEC §4.3
func TestAVoiceTheEmbedderFailedOnIsRefusedAsAFailure(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1700, AudioRef: "blob://mic/hang-on-stop", Energy: 0.9, Partial: "hang on stop",
		SpeakerFailed: true, EchoJudged: true, EchoScore: 0.18, EchoLagMS: 90,
	})
	if err != nil || ok {
		t.Fatalf("barge-in: ok=%v err=%v, want it refused while nothing can say who spoke", ok, err)
	}
	close(r.speaker.release)
	wait(t, errc)

	rejected := r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected)
	if len(rejected) != 1 || rejected[0].Fields["stage"] != "speaker_id_failed" {
		t.Fatalf("barge_in_rejected = %v, want one at speaker_id_failed", rejected)
	}
	if st := r.state(t, s.ConversationID()); st.Interrupted {
		t.Error("a voice nothing judged stopped the forecast")
	}
}

// The echo stage still runs first: the device's own voice is refused as an
// echo whether or not the embedder would have failed on it.
//
// verifies SPEC §4.3
func TestAnEchoTheEmbedderFailedOnIsStillAnEcho(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 2100, AudioRef: "blob://mic/forecast-echo", Energy: 0.9, Partial: "clouding over by noon",
		SpeakerFailed: true, Echo: true, EchoJudged: true, EchoScore: 0.77, EchoLagMS: 140,
	})
	if err != nil || ok {
		t.Fatalf("barge-in: ok=%v err=%v, want the echo refused", ok, err)
	}
	close(r.speaker.release)
	wait(t, errc)

	if got := r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected); len(got) != 1 || got[0].Fields["stage"] != "echo" {
		t.Errorf("barge_in_rejected = %v, want one at echo", got)
	}
}

// A house with no identifier at all skips the speaker stage (ADR-0031), so a
// failure flag from nowhere changes nothing there.
//
// verifies SPEC §4.3
func TestWithoutSpeakerIdentificationThereIsNoFailureToRefuse(t *testing.T) {
	r, s, errc := forecasting(t, func(cfg *session.Config) {
		kitchenHousehold(cfg)
		cfg.Gate.SpeakerIDUnavailable = true
	})

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/hang-on", Energy: 0.9, Partial: "hang on", SpeakerFailed: true,
	})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v, want it admitted as before", ok, err)
	}
	wait(t, errc)
	if f := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected).Fields; f["speaker_stage_skipped"] != "true" {
		t.Errorf("barge_in_detected = %v, want the stage marked skipped", f)
	}
}
