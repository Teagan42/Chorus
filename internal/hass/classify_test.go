package hass_test

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
)

const (
	openGarage = `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`
	openBlinds = `{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`
)

// coversOn is a Home Assistant with the garage door and the living-room
// blinds and window, answering each as 2026.10 does.
func coversOn() *transport {
	wire := newTransport(http.StatusOK, `[]`)
	wire.route("/api/states/cover.garage_door", http.StatusOK, garageClosed)
	wire.route("/api/states/cover.living_room_blinds", http.StatusOK, livingRoomBlinds)
	wire.route("/api/states/cover.living_room_window", http.StatusOK, livingRoomWindow)
	wire.route("/api/states/cover.shed_door", http.StatusNotFound, entityNotFound)
	wire.route("/api/services/cover/open_cover", http.StatusOK, garageOpened)
	return wire
}

func classifier(t *testing.T, wire *transport) session.Classifier {
	t.Helper()
	c, ok := hass.Tools(clientOn(t, wire))["ha_call_service"].(session.Classifier)
	if !ok {
		t.Fatal("ha_call_service cannot say what a call acts on")
	}
	return c
}

// The class is HA's own device_class, read from the entity the call names:
// the garage door says garage, the blinds say blind, and the window, which
// nobody classed, says nothing (ADR-0041).
//
// verifies SPEC §6
func TestTheCoverSaysWhatItIs(t *testing.T) {
	wire := coversOn()
	c := classifier(t, wire)
	for _, tc := range []struct {
		args string
		want []string
	}{
		{openGarage, []string{"garage"}},
		{`{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door","confirmation":"cf_9b2e4d71"}`, []string{"garage"}},
		{openBlinds, []string{"blind"}},
		{`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_window","area_id":null}`, nil},
	} {
		got, err := c.Classify(context.Background(), tc.args)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Classify(%s) = %q, %v; want %q", tc.args, got, err, tc.want)
		}
		if req := wire.last(t); req.method != http.MethodGet || !strings.HasPrefix(req.path, "/api/states/cover.") {
			t.Errorf("classifying %s asked %s %s, want the cover's state", tc.args, req.method, req.path)
		}
	}
	if wire.count() != 4 {
		t.Errorf("home assistant was asked %d times, want once per call", wire.count())
	}
}

// A target the REST API cannot classify is an error, and an error holds the
// call: an area's covers, a target tucked into data, a cover HA does not
// have, an HA that is down.
//
// verifies SPEC §6
func TestATargetThatCannotBeReadIsAnError(t *testing.T) {
	wire := coversOn()
	c := classifier(t, wire)
	for _, args := range []string{
		`{"domain":"cover","service":"open_cover","area_id":"garage"}`,
		`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds","data":{"area_id":"garage"}}`,
		`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds","data":{"device_id":"8f3c2a1e5d7b4c9a"}}`,
		`{"domain":"cover","service":"open_cover","entity_id":"cover.shed_door"}`,
		`{"domain":"cover","service":"open_cover","entity_id":"garage door"}`,
		`{"domain":"cover","service":"open_cover","entity_id":["cover.garage_door","cover.living_room_blinds"]}`,
		`{"domain":"cover","service":"open_cover","entity_id":"cover.garage_d`,
	} {
		if got, err := c.Classify(context.Background(), args); err == nil {
			t.Errorf("Classify(%s) = %q, want an error", args, got)
		}
	}

	down := newTransport(http.StatusServiceUnavailable, "503: Service Unavailable")
	if got, err := classifier(t, down).Classify(context.Background(), openGarage); err == nil {
		t.Errorf("Classify with HA down = %q, want an error", got)
	}
}

// A target in data would reach Home Assistant beside the one the gate read,
// so the blinds' yes could open the garage too. The call is refused before
// it is sent.
//
// verifies SPEC §6
func TestATargetInDataIsRefused(t *testing.T) {
	wire := coversOn()
	tools := hass.Tools(clientOn(t, wire))
	for _, args := range []string{
		`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds","data":{"area_id":"garage"}}`,
		`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds","data":{"entity_id":["cover.garage_door"]}}`,
		`{"domain":"cover","service":"open_cover","area_id":"living_room","data":{"device_id":"8f3c2a1e5d7b4c9a"}}`,
	} {
		_, err := tools["ha_call_service"].Invoke(context.Background(), args)
		if err == nil || !strings.Contains(err.Error(), "name the target in entity_id or area_id") {
			t.Errorf("Invoke(%s) = %v, want it refused as a target in data", args, err)
		}
	}
	if n := wire.count(); n != 0 {
		t.Errorf("home assistant was sent %d requests", n)
	}
}

// coverModel opens whichever cover Alice names, asks when the call is held,
// and calls again with the nonce once she says yes.
type coverModel struct{}

func (coverModel) Turn(_ context.Context, in session.Input) (<-chan session.Action, error) {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	var acts []session.Action
	last := in.Dialogue[len(in.Dialogue)-1]
	switch {
	case last.Kind == journal.EntryHeard && last.Text == "open the garage door":
		acts = []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: openGarage}, done}
	case last.Kind == journal.EntryHeard && last.Text == "open the living room blinds":
		acts = []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: openBlinds}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
		acts = []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "Open the garage door?", Last: true}, done}
	case last.Kind == journal.EntryHeard && last.Text == "yes, open it":
		var r struct {
			Nonce string `json:"nonce"`
		}
		for _, e := range in.Dialogue {
			if e.Outcome == "confirmation_required" {
				_ = json.Unmarshal([]byte(e.Result), &r)
			}
		}
		args := strings.TrimSuffix(openGarage, "}") + `,"confirmation":"` + r.Nonce + `"}`
		acts = []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: args}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "ok":
		acts = []session.Action{session.SpeechDelta{CallID: "call_s2", Text: "It's open.", Last: true}, done}
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

// Alice asks the kitchen to open the garage door. Home Assistant is asked
// what the cover is, and is not asked to open it until she has said yes.
//
// verifies SPEC §6
func TestHomeAssistantIsNotAskedToOpenTheGarageUntilAliceSaysYes(t *testing.T) {
	wire := coversOn()
	r := newRigOn(t, wire, coverModel{})
	s := r.open(t)

	wait(t, heard(s, "open the garage door"))
	if got := wire.last(t); wire.count() != 1 || got.method != http.MethodGet || got.path != "/api/states/cover.garage_door" {
		t.Fatalf("home assistant saw %d requests, last %s %s: want only the garage's state", wire.count(), got.method, got.path)
	}
	if c := r.awaitCall(t, s.ConversationID(), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("first call = %+v, want it held", c)
	}

	wait(t, heard(s, "yes, open it"))
	// The re-call runs on the log's word: HA is not asked again what it is.
	if n := wire.count(); n != 2 {
		t.Fatalf("home assistant got %d requests, want the state and then the one open", n)
	}
	req := wire.last(t)
	if req.method != http.MethodPost || req.path != "/api/services/cover/open_cover" || req.body != `{"entity_id":"cover.garage_door"}` {
		t.Errorf("request = %s %s %s, want the garage opened without the nonce", req.method, req.path, req.body)
	}
	if c := r.awaitCall(t, s.ConversationID(), "call_c2"); c.Outcome != "ok" || !strings.Contains(c.Result, `"state":"open"`) {
		t.Errorf("confirmed call = %+v, want ok with the garage open", c)
	}
	st := r.state(t, s.ConversationID())
	if got := st.Confirmations; len(got) != 1 || got[0].Answer != "yes, open it" || got[0].AnsweredBy != "alice" || got[0].RedeemedBy != "call_c2" {
		t.Errorf("confirmations = %+v, want Alice's yes redeemed by call_c2", got)
	}
}

// The living-room blinds are opened by the same service as the garage, and
// are opened straight away.
//
// verifies SPEC §6
func TestTheLivingRoomBlindsOpenStraightAway(t *testing.T) {
	wire := coversOn()
	wire.route("/api/services/cover/open_cover", http.StatusOK, blindsOpening)
	r := newRigOn(t, wire, coverModel{})
	s := r.open(t)

	wait(t, heard(s, "open the living room blinds"))

	if c := r.awaitCall(t, s.ConversationID(), "call_c1"); c.Outcome != "ok" || !strings.Contains(c.Result, "cover.living_room_blinds") {
		t.Errorf("call = %+v, want the blinds opened", c)
	}
	req := wire.last(t)
	if wire.count() != 2 || req.path != "/api/services/cover/open_cover" || req.body != `{"entity_id":"cover.living_room_blinds"}` {
		t.Errorf("home assistant saw %d requests ending %s %s, want the blinds' state then the open", wire.count(), req.path, req.body)
	}
	if got := r.state(t, s.ConversationID()).Confirmations; len(got) != 0 {
		t.Errorf("confirmations = %+v, want none for the blinds", got)
	}
}
