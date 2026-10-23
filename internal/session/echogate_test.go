package session_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// The forecast comes back through the kitchen's microphone and the ear reads
// "stop" in it -- "...clouding over by noon" is close enough. It matched what
// the DAC was playing, so the echo stage refuses it before anything asks
// whose voice it is, the forecast plays on, and the refusal keeps the score
// and where the match was found, for tuning the threshold on a real room.
//
// verifies SPEC §4.3
func TestTheDevicesOwnVoiceIsRefusedAtTheEchoStage(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 2100, AudioRef: "blob://mic/forecast-echo", Energy: 0.9, Partial: "Stop.",
		Echo: true, EchoJudged: true, EchoScore: 0.74, EchoLagMS: 140,
	})
	if err != nil || ok {
		t.Fatalf("barge-in: ok=%v err=%v, want the echo refused", ok, err)
	}
	close(r.speaker.release)
	wait(t, errc)

	rejected := r.ofKind(t, s.ConversationID(), journal.KindBargeInRejected)
	if len(rejected) != 1 {
		t.Fatalf("barge_in_rejected = %v, want one", rejected)
	}
	f := rejected[0].Fields
	if f["stage"] != "echo" || f["echo_score"] != "0.74" || f["echo_lag_ms"] != "140" || f["hot_word"] != "stop" {
		t.Errorf("barge_in_rejected = %v, want echo at 0.74, 140 ms, naming the stop", f)
	}
	if st := r.state(t, s.ConversationID()); st.Interrupted {
		t.Error("the device's own voice stopped the forecast")
	}
}

// The echo stage comes before the speaker stage, and an identified voice
// does not get past it: a household member's voice the embedder found in
// the device's own speech -- Kokoro's voice landing near an enrolled one --
// is still the device talking.
//
// verifies SPEC §4.3
func TestAnEchoTheEmbedderPutWithTeaganIsStillAnEcho(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1800, AudioRef: "blob://mic/forecast-echo", SpeakerID: "teagan", Energy: 0.9,
		Partial: "with rain from three", Echo: true, EchoJudged: true, EchoScore: 0.81, EchoLagMS: 150,
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

// With nothing identifying speakers (ADR-0031) the speaker stage is skipped,
// and before the echo stage that left nothing between the device's own voice
// and a barge-in: any echo of two words stopped the answer it was an echo
// of. The echo stage runs whatever the speaker stage does.
//
// verifies SPEC §4.3
func TestWithoutSpeakerIdentificationTheEchoIsStillRefused(t *testing.T) {
	r, s, errc := forecasting(t, func(cfg *session.Config) {
		kitchenHousehold(cfg)
		cfg.Gate.SpeakerIDUnavailable = true
	})

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1500, AudioRef: "blob://mic/forecast-echo", Energy: 0.9, Partial: "clouding over by noon",
		Echo: true, EchoJudged: true, EchoScore: 0.69, EchoLagMS: 130,
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

// Teagan's "hang on" was compared too and is nothing like the forecast: it
// stops speech, and the detection keeps its score beside the rejections',
// because a real barge-in's score is the other half of what tunes the
// threshold. A candidate nothing compared carries no score at all.
//
// verifies SPEC §4.3
func TestAPersonsScoreIsKeptOnTheDetection(t *testing.T) {
	r, s, errc := forecasting(t, kitchenHousehold)

	ok, err := s.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/hang-on", SpeakerID: "teagan", Energy: 0.9, Partial: "hang on",
		EchoJudged: true, EchoScore: 0.21, EchoLagMS: -40,
	})
	if err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v, want Teagan admitted", ok, err)
	}
	wait(t, errc)

	f := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected).Fields
	if f["echo_score"] != "0.21" || f["echo_lag_ms"] != "-40" {
		t.Errorf("barge_in_detected = %v, want her score and lag kept", f)
	}

	r2, s2, errc2 := forecasting(t, kitchenHousehold)
	if ok, err := s2.BargeIn(t.Context(), session.Candidate{
		PositionMS: 1300, AudioRef: "blob://mic/hang-on", SpeakerID: "teagan", Energy: 0.9, Partial: "hang on",
	}); err != nil || !ok {
		t.Fatalf("unjudged barge-in: ok=%v err=%v, want it admitted", ok, err)
	}
	wait(t, errc2)
	f = r2.eventOf(t, s2.ConversationID(), journal.KindBargeInDetected).Fields
	if _, scored := f["echo_score"]; scored {
		t.Errorf("barge_in_detected = %v, want no score where nothing was compared", f)
	}
}
