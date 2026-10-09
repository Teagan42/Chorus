package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

// Alan asks the kitchen satellite to unlock the front door. Nothing reaches
// Home Assistant until he has heard the question and said yes; then the
// daemon sends the unlock it would have sent without the gate, and says so.
//
// verifies SPEC §6
func TestTheFrontDoorUnlocksOnlyAfterAlanSaysYes(t *testing.T) {
	const (
		unlock   = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
		question = "Unlock the front door?"
		answer   = "Done, the front door is unlocked."
	)
	ha := &homeAssistant{}
	client, err := hass.New(hass.Config{
		BaseURL: "http://homeassistant.invalid:8123", Token: "ha-test-token",
		HTTP: &http.Client{Transport: ha},
	})
	if err != nil {
		t.Fatalf("hass: %v", err)
	}
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlock}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: question, Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			var held struct {
				Nonce string `json:"nonce"`
			}
			for _, e := range in.Dialogue {
				if e.Outcome == "confirmation_required" {
					_ = json.Unmarshal([]byte(e.Result), &held)
				}
			}
			args := strings.TrimSuffix(unlock, "}") + `,"confirmation":"` + held.Nonce + `"}`
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: args}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			return []session.Action{session.SpeechDelta{CallID: "call_s2", Text: answer, Last: true}, done}
		}
		return []session.Action{done}
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = hass.Tools(client)
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("unlock the front door", alan))
	dev.AwaitTTS(t, 2*len(question))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 1)
	if got := ha.paths(); len(got) != 0 {
		t.Fatalf("home assistant was asked %q before Alan said yes", got)
	}

	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 2*len(question)+2*len(answer))
	dev.PlayAll(t)
	r.store.awaitKind(t, journal.KindSpeechSpoken, 2)

	if got := ha.paths(); len(got) != 1 || got[0] != "POST /api/services/lock/unlock" {
		t.Fatalf("home assistant was asked %q, want the front door unlocked once", got)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(ha.sent()[0]), &body); err != nil {
		t.Fatalf("unlock body %q: %v", ha.sent()[0], err)
	}
	if len(body) != 1 || body["entity_id"] != "lock.front_door" {
		t.Errorf("unlock body = %v, want only the front door: no nonce", body)
	}

	var spoken []string
	for _, e := range r.store.ofKind(journal.KindSpeechSpoken) {
		spoken = append(spoken, e.Fields["text"])
	}
	if len(spoken) != 2 || spoken[0] != question || spoken[1] != answer {
		t.Errorf("spoken = %q, want %q then %q", spoken, question, answer)
	}
	requested := r.store.ofKind(journal.KindConfirmationRequested)
	given := r.store.ofKind(journal.KindConfirmationGiven)
	if len(requested) != 1 || len(given) != 1 || given[0].Fields["nonce"] != requested[0].Fields["nonce"] {
		t.Errorf("requested %d, given %d: want the one nonce handed out and redeemed", len(requested), len(given))
	}
}
