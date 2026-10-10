package journal_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// The front door, as the model asks to unlock it, and as the session records
// the arguments it holds the nonce against.
const (
	frontDoor     = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
	frontDoorRest = `{"domain":"lock","entity_id":"lock.front_door","service":"unlock"}`
	backDoorRest  = `{"domain":"lock","entity_id":"lock.back_door","service":"unlock"}`
)

// Teagan asks the kitchen to unlock the front door. The call is held and
// handed a nonce; the model asks, and Teagan's answer is the next thing the
// log hears.
func heldFrontDoor() []journal.Event {
	return []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "unlock the front door", "speaker_id": "teagan"}),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_call_service", "call_id": "call_c1", "args_json": frontDoor}),
		ev(journal.KindConfirmationRequested, map[string]string{"call_id": "call_c1", "nonce": "cf_4c1e9a07"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c1", "outcome": "confirmation_required", "result_json": `{"confirmation_required":true,"nonce":"cf_4c1e9a07"}`}),
		ev(journal.KindToolCalled, map[string]string{"tool": "speak", "call_id": "call_s1", "args_json": `{"text":"Unlock the front door?"}`}),
		ev(journal.KindSpeechSpoken, map[string]string{"text": "Unlock the front door?", "frames_played": "20800", "call_id": "call_s1"}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_s1", "outcome": "ok"}),
	}
}

func answer(text, speaker string) journal.Event {
	return ev(journal.KindUtteranceTranscribed, map[string]string{"text": text, "speaker_id": speaker})
}

// A nonce is good for the call it was handed to, in the turn of the next
// thing the person says, once. Each refusal is a reason the session hands
// back to the model with a fresh nonce.
//
// verifies SPEC §6
func TestANonceIsRedeemedOnlyAfterThePersonAnswers(t *testing.T) {
	held := heldFrontDoor()
	cases := []struct {
		why   string
		after []journal.Event
		nonce string
		args  string
		want  string
	}{
		{"before Teagan has said anything", nil, "cf_4c1e9a07", frontDoorRest, journal.RefusedNotAnswered},
		{"after Teagan's yes", []journal.Event{answer("yes please", "teagan")}, "cf_4c1e9a07", frontDoorRest, ""},
		{"on the back door instead", []journal.Event{answer("yes please", "teagan")}, "cf_4c1e9a07", backDoorRest, journal.RefusedArgsChanged},
		{"on a nonce nobody was handed", []journal.Event{answer("yes please", "teagan")}, "cf_00000000", frontDoorRest, journal.RefusedUnknown},
		{"once the conversation has moved on", []journal.Event{
			answer("hang on, who is it", "teagan"),
			answer("okay yes", "teagan"),
		}, "cf_4c1e9a07", frontDoorRest, journal.RefusedExpired},
		{"a second time", []journal.Event{
			answer("yes please", "teagan"),
			ev(journal.KindToolCalled, map[string]string{"tool": "ha_call_service", "call_id": "call_c2", "args_json": `{"domain":"lock","service":"unlock","entity_id":"lock.front_door","confirmation":"cf_4c1e9a07"}`}),
			ev(journal.KindConfirmationGiven, map[string]string{"call_id": "call_c2", "nonce": "cf_4c1e9a07"}),
		}, "cf_4c1e9a07", frontDoorRest, journal.RefusedUsed},
	}
	for _, c := range cases {
		s := reduceAll(t, append(append([]journal.Event(nil), held...), c.after...))
		if got := s.Redeemable(c.nonce, "ha_call_service", c.args); got != c.want {
			t.Errorf("%s: Redeemable = %q, want %q", c.why, got, c.want)
		}
	}
}

// A refused nonce is spent by its try. The model switched from the front
// door to the back door before anyone answered, so the back door got a
// nonce of its own; Teagan's yes to the back-door question must not unlock
// the front door on the nonce the switch was refused with.
//
// verifies SPEC §6
func TestARefusedNonceCannotRideTheNextAnswer(t *testing.T) {
	s := reduceAll(t, append(heldFrontDoor()[:5],
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_call_service", "call_id": "call_c2", "args_json": `{"domain":"lock","service":"unlock","entity_id":"lock.back_door","confirmation":"cf_4c1e9a07"}`}),
		ev(journal.KindConfirmationRequested, map[string]string{"call_id": "call_c2", "nonce": "cf_9d02b5e1", "presented": "cf_4c1e9a07", "refused": journal.RefusedArgsChanged}),
		ev(journal.KindToolResult, map[string]string{"call_id": "call_c2", "outcome": "confirmation_required"}),
		answer("yes, the back door", "teagan"),
	))
	if got := s.Redeemable("cf_4c1e9a07", "ha_call_service", frontDoorRest); got != journal.RefusedUsed {
		t.Errorf("front door on the refused nonce = %q, want %q", got, journal.RefusedUsed)
	}
	if got := s.Redeemable("cf_9d02b5e1", "ha_call_service", backDoorRest); got != "" {
		t.Errorf("back door on its own nonce = %q, want it redeemable", got)
	}
	if c := s.Confirmations[0]; c.RefusedBy != "call_c2" || c.Heard != 0 {
		t.Errorf("front door's confirmation = %+v, want spent by call_c2 and not counting the answer", c)
	}
}

// The audit: which call was held, what the person said next and who said
// it, and which call ran on the nonce. All of it is replayed from the log.
//
// verifies SPEC §6, §8
func TestTheLogSaysWhoSaidYesToWhat(t *testing.T) {
	s := reduceAll(t, append(heldFrontDoor(),
		answer("yep", "teagan"),
		ev(journal.KindToolCalled, map[string]string{"tool": "ha_call_service", "call_id": "call_c2", "args_json": `{"confirmation":"cf_4c1e9a07","entity_id":"lock.front_door","service":"unlock","domain":"lock"}`}),
		ev(journal.KindConfirmationGiven, map[string]string{"call_id": "call_c2", "nonce": "cf_4c1e9a07"}),
		answer("and turn the porch light on", "alan"),
	))
	want := []journal.Confirmation{{
		Nonce: "cf_4c1e9a07", CallID: "call_c1", Tool: "ha_call_service", Args: frontDoorRest,
		Heard: 1, Answer: "yep", AnsweredBy: "teagan", RedeemedBy: "call_c2",
	}}
	if len(s.Confirmations) != 1 || s.Confirmations[0] != want[0] {
		t.Errorf("confirmations = %+v, want %+v", s.Confirmations, want)
	}
}

// A confirmation for a call the log never recorded cannot stand on anything.
//
// verifies SPEC §8
func TestAConfirmationNeedsItsCall(t *testing.T) {
	var s journal.State
	_, err := journal.Reduce(s, journal.Event{Seq: 1, Kind: journal.KindConfirmationRequested, Fields: map[string]string{"call_id": "call_c9", "nonce": "cf_4c1e9a07"}})
	if err == nil {
		t.Error("a request for an unrecorded call reduced")
	}
	_, err = journal.Reduce(s, journal.Event{Seq: 1, Kind: journal.KindConfirmationGiven, Fields: map[string]string{"call_id": "call_c9", "nonce": "cf_4c1e9a07"}})
	if err == nil {
		t.Error("a yes on an unissued nonce reduced")
	}
}
