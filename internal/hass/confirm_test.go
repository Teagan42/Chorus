package hass_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

const disarm = `{"domain":"alarm_control_panel","service":"alarm_disarm","entity_id":"alarm_control_panel.house","data":{"code":"4512"}}`

// alarmModel disarms the house alarm when Alice asks, asks her when the call
// is held, and calls again with the nonce once she has answered.
type alarmModel struct {
	mu    sync.Mutex
	asked int
}

func (m *alarmModel) Turn(_ context.Context, in session.Input) (<-chan session.Action, error) {
	m.mu.Lock()
	m.asked++
	m.mu.Unlock()
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	var acts []session.Action
	last := in.Dialogue[len(in.Dialogue)-1]
	switch {
	case last.Kind == journal.EntryHeard && last.Text == "disarm the alarm":
		acts = []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: disarm}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
		acts = []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "Disarm the house alarm?", Last: true}, done}
	case last.Kind == journal.EntryHeard && last.Text == "yes, disarm it":
		var r struct {
			Nonce string `json:"nonce"`
		}
		for _, e := range in.Dialogue {
			if e.Outcome == "confirmation_required" {
				_ = json.Unmarshal([]byte(e.Result), &r)
			}
		}
		args := strings.TrimSuffix(disarm, "}") + `,"confirmation":"` + r.Nonce + `"}`
		acts = []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: args}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "ok":
		acts = []session.Action{session.SpeechDelta{CallID: "call_s2", Text: "The alarm is off.", Last: true}, done}
	default:
		acts = []session.Action{done}
	}
	out := make(chan session.Action, len(acts))
	for _, a := range acts {
		out <- a
	}
	close(out)
	return out, nil
}

// Alice comes home and asks the kitchen to disarm the alarm. Home Assistant
// hears nothing until she has said yes, and then hears the service call it
// would have got without the gate: no nonce in the body.
//
// verifies SPEC §6
func TestHomeAssistantIsNotAskedToDisarmUntilAliceSaysYes(t *testing.T) {
	wire := newTransport(http.StatusOK, `[{"entity_id":"alarm_control_panel.house","state":"disarmed","attributes":{"friendly_name":"House Alarm"}}]`)
	r := newRigOn(t, wire, &alarmModel{})
	s := r.open(t)

	wait(t, heard(s, "disarm the alarm"))
	if n := wire.count(); n != 0 {
		t.Fatalf("home assistant got %d requests before Alice said yes", n)
	}
	if c := r.awaitCall(t, s.ConversationID(), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("first call = %+v, want it held", c)
	}

	wait(t, heard(s, "yes, disarm it"))
	if n := wire.count(); n != 1 {
		t.Fatalf("home assistant got %d requests, want the one disarm", n)
	}
	req := wire.last(t)
	if req.method != http.MethodPost || req.path != "/api/services/alarm_control_panel/alarm_disarm" {
		t.Errorf("request = %s %s, want POST /api/services/alarm_control_panel/alarm_disarm", req.method, req.path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.body), &body); err != nil {
		t.Fatalf("body %q: %v", req.body, err)
	}
	if body["entity_id"] != "alarm_control_panel.house" || body["code"] != "4512" {
		t.Errorf("body = %v, want the house alarm and its code", body)
	}
	if _, leaked := body["confirmation"]; leaked {
		t.Errorf("body = %v: the nonce reached home assistant", body)
	}
	if c := r.awaitCall(t, s.ConversationID(), "call_c2"); c.Outcome != "ok" {
		t.Errorf("confirmed call = %+v, want ok", c)
	}
	st := r.state(t, s.ConversationID())
	if got := st.Confirmations; len(got) != 1 || got[0].Answer != "yes, disarm it" || got[0].AnsweredBy != "alice" || got[0].RedeemedBy != "call_c2" {
		t.Errorf("confirmations = %+v, want Alice's yes redeemed by call_c2", got)
	}
}
