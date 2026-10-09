package registry_test

import (
	"testing"

	"github.com/teaganglenn/chorus/internal/registry"
)

// The household's own calls through Home Assistant: the front door and the
// alarm wait for a yes, the kitchen lights and the porch light do not.
//
// verifies SPEC §6
func TestOnlyTheDoorsAndTheAlarmWaitForAYes(t *testing.T) {
	ha := registry.Specs["ha_call_service"]
	cases := []struct {
		why  string
		args string
		want bool
	}{
		{"unlocking the front door", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`, true},
		{"opening the back door's latch", `{"service":"open","domain":"lock","entity_id":"lock.back_door"}`, true},
		{"disarming the alarm", `{"domain":"alarm_control_panel","service":"alarm_disarm","entity_id":"alarm_control_panel.house","data":{"code":"4512"}}`, true},
		{"a model that shouts", `{"domain":"LOCK","service":"Unlock","entity_id":"lock.front_door"}`, true},
		{"arguments nobody can read", `{"domain":"lock","service":"unl`, true},
		{"locking the front door", `{"domain":"lock","service":"lock","entity_id":"lock.front_door"}`, false},
		{"the kitchen lights", `{"domain":"light","service":"turn_on","entity_id":"light.kitchen","data":{"brightness_pct":50}}`, false},
		{"the porch light", `{"domain":"switch","service":"turn_off","entity_id":"switch.porch_light"}`, false},
		{"arming the alarm for the night", `{"domain":"alarm_control_panel","service":"alarm_arm_night","entity_id":"alarm_control_panel.house"}`, false},
	}
	for _, c := range cases {
		if got := ha.NeedsConfirmation(c.args); got != c.want {
			t.Errorf("%s: NeedsConfirmation = %t, want %t", c.why, got, c.want)
		}
	}
	if registry.Specs["ha_get_state"].NeedsConfirmation(`{"entity_id":"lock.front_door"}`) {
		t.Error("reading the front door's state should not wait for a yes")
	}
	if !(registry.ToolSpec{RequiresConfirmation: true}).NeedsConfirmation(`{}`) {
		t.Error("a tool that requires confirmation should hold every call")
	}
}

// The model is offered the nonce on the tool it can be held on.
//
// verifies SPEC §6
func TestAHeldToolOffersTheNonce(t *testing.T) {
	offered := func(name string) bool {
		for _, p := range registry.Specs[name].Params {
			if p.Name == registry.ConfirmationParam {
				return true
			}
		}
		return false
	}
	if !offered("ha_call_service") {
		t.Error("ha_call_service should offer the confirmation argument")
	}
	if offered("ha_get_state") {
		t.Error("ha_get_state is never held, so it should not offer one")
	}
}

// The yes is checked against the call that was held, so the arguments are
// compared as the call made them, whatever order the model wrote them in.
//
// verifies SPEC §6
func TestTheNonceComesOffAndTheRestComparesEqual(t *testing.T) {
	first, nonce, err := registry.Unconfirmed(`{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`)
	if err != nil || nonce != "" {
		t.Fatalf("Unconfirmed = %q, %q, %v", first, nonce, err)
	}
	again, nonce, err := registry.Unconfirmed(`{ "entity_id": "lock.front_door", "confirmation": "cf_4c1e9a07", "service": "unlock", "domain": "lock" }`)
	if err != nil {
		t.Fatalf("Unconfirmed: %v", err)
	}
	if nonce != "cf_4c1e9a07" {
		t.Errorf("nonce = %q, want cf_4c1e9a07", nonce)
	}
	if again != first {
		t.Errorf("the re-call reads %s, the held call %s: the same door should compare equal", again, first)
	}

	// Values are kept as written: the rest is what the tool receives.
	got, _, err := registry.Unconfirmed(`{"domain":"alarm_control_panel","service":"alarm_disarm","data":{"code":"004512","delay":30.0},"confirmation":"cf_77d01b2e"}`)
	if err != nil {
		t.Fatalf("Unconfirmed: %v", err)
	}
	if want := `{"data":{"code":"004512","delay":30.0},"domain":"alarm_control_panel","service":"alarm_disarm"}`; got != want {
		t.Errorf("rest = %s, want %s", got, want)
	}
}

// A nonce that is not a string, or arguments that are not one JSON object,
// are refused rather than guessed at.
//
// verifies SPEC §6
func TestArgumentsThatCannotBeReadAreRefused(t *testing.T) {
	for _, args := range []string{
		`{"domain":"lock","service":"unlock","confirmation":4512}`,
		`{"domain":"lock","service":"unlock"} {"confirmation":"cf_4c1e9a07"}`,
		`{"domain":"lock","service":`,
	} {
		if _, _, err := registry.Unconfirmed(args); err == nil {
			t.Errorf("Unconfirmed(%s) accepted it", args)
		}
	}
}
