package registry_test

import (
	"errors"
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

// The garage door and the living-room blinds are both covers, opened by the
// same cover.open_cover. What tells them apart is the class Home Assistant
// gives each, read from the cover the call names (ADR-0041).
//
// verifies SPEC §6
func TestTheGarageWaitsForAYesAndTheBlindsDoNot(t *testing.T) {
	ha := registry.Specs["ha_call_service"]
	classed := func(classes ...string) registry.Classes {
		return func() ([]string, error) { return classes, nil }
	}
	unreadable := func() ([]string, error) {
		return nil, errors.New("get state cover.garage_door: 503 Service Unavailable")
	}
	cases := []struct {
		why     string
		args    string
		classes registry.Classes
		want    bool
	}{
		{"opening the garage door", `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`, classed("garage"), true},
		{"opening the side gate", `{"domain":"cover","service":"open_cover","entity_id":"cover.side_gate"}`, classed("gate"), true},
		{"opening the motorised front door", `{"domain":"cover","service":"open_cover","entity_id":"cover.front_door"}`, classed("door"), true},
		{"toggling the garage from closed", `{"domain":"cover","service":"toggle","entity_id":"cover.garage_door"}`, classed("garage"), true},
		{"raising the garage halfway", `{"domain":"cover","service":"set_cover_position","entity_id":"cover.garage_door","data":{"position":50}}`, classed("garage"), true},
		{"toggling the garage through homeassistant", `{"domain":"homeassistant","service":"toggle","entity_id":"cover.garage_door"}`, classed("garage"), true},
		{"a class written as HA never would", `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`, classed(" Garage "), true},
		{"a garage nobody can read", `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`, unreadable, true},
		{"a garage nothing can classify", `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`, nil, true},
		{"opening the living-room blinds", `{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`, classed("blind"), false},
		{"opening the kitchen window, which has no class", `{"domain":"cover","service":"open_cover","entity_id":"cover.kitchen_window"}`, classed(), false},
		{"closing the garage door", `{"domain":"cover","service":"close_cover","entity_id":"cover.garage_door"}`, classed("garage"), false},
		{"the kitchen lights, whatever they claim to be", `{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}`, classed("garage"), false},
		{"unlocking the front door, whatever it is", `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`, classed(), true},
	}
	for _, c := range cases {
		if got := ha.NeedsConfirmationOf(c.args, c.classes); got != c.want {
			t.Errorf("%s: NeedsConfirmationOf = %t, want %t", c.why, got, c.want)
		}
	}
}

// The garage's class is read once per call, and not at all for a call no
// class could change: every read is a round trip to Home Assistant before
// the person hears anything.
//
// verifies SPEC §6
func TestTheTargetIsReadOnlyWhenItDecides(t *testing.T) {
	ha := registry.Specs["ha_call_service"]
	reads := 0
	counted := func() ([]string, error) { reads++; return []string{"blind"}, nil }

	ha.NeedsConfirmationOf(`{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}`, counted)
	ha.NeedsConfirmationOf(`{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`, counted)
	if reads != 0 {
		t.Errorf("the kitchen lights and the front door read a class %d times, want none", reads)
	}
	ha.NeedsConfirmationOf(`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`, counted)
	if reads != 1 {
		t.Errorf("the living-room blinds read their class %d times, want once", reads)
	}

	// Two entries that both match the call still read the target once.
	both := registry.ToolSpec{ConfirmWhen: []registry.ConfirmRule{
		{Args: map[string]string{"domain": "cover"}, TargetClass: []string{"garage"}},
		{Args: map[string]string{"service": "open_cover"}, TargetClass: []string{"gate"}},
	}}
	reads = 0
	both.NeedsConfirmationOf(`{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`, counted)
	if reads != 1 {
		t.Errorf("two matching entries read the class %d times, want once", reads)
	}
}

// The model is offered the nonce on the tool it can be held on.
//
// verifies SPEC §6
func TestAHeldToolOffersTheNonce(t *testing.T) {
	offered := func(name string) bool {
		for _, p := range registry.Specs[name].ModelParams {
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
	first, err := registry.Split(`{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`)
	if err != nil || first.Nonce != "" {
		t.Fatalf("Split = %+v, %v", first, err)
	}
	again, err := registry.Split(`{ "entity_id": "lock.front_door", "confirmation": "cf_4c1e9a07", "service": "unlock", "domain": "lock" }`)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if again.Nonce != "cf_4c1e9a07" {
		t.Errorf("nonce = %q, want cf_4c1e9a07", again.Nonce)
	}
	if again.Rest != first.Rest {
		t.Errorf("the re-call reads %s, the held call %s: the same door should compare equal", again.Rest, first.Rest)
	}

	// Values are kept as written: the rest is what the tool receives.
	got, err := registry.Split(`{"domain":"alarm_control_panel","service":"alarm_disarm","data":{"code":"004512","delay":30.0},"confirmation":"cf_77d01b2e"}`)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	if want := `{"data":{"code":"004512","delay":30.0},"domain":"alarm_control_panel","service":"alarm_disarm"}`; got.Rest != want {
		t.Errorf("rest = %s, want %s", got.Rest, want)
	}
}

// What to say while a slow tool works comes off as well, and does not count
// as an argument: "Searching the library." and "One moment." are the same
// search, and the library does not want either.
//
// verifies SPEC §6
func TestTheAcknowledgementComesOffWithTheNonce(t *testing.T) {
	got, err := registry.Split(`{"query":"led zeppelin","acknowledgement":"Searching the library.","limit":5}`)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}
	want := registry.Orchestrated{Rest: `{"limit":5,"query":"led zeppelin"}`, Acknowledgement: "Searching the library."}
	if got != want {
		t.Errorf("Split = %+v, want %+v", got, want)
	}
	if _, err := registry.Split(`{"query":"led zeppelin","acknowledgement":["one","moment"]}`); err == nil {
		t.Error("an acknowledgement that is not a string was accepted")
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
		if _, err := registry.Split(args); err == nil {
			t.Errorf("Split(%s) accepted it", args)
		}
	}
}

// An executor checks the calls it receives against Params, as the Home
// Assistant adapter does, and the session strips the nonce and the
// acknowledgement before it invokes one. Params must not declare either, or
// every slow call would fail as missing a required argument the executor
// never gets. The model is offered them in ModelParams.
//
// verifies SPEC §6
func TestExecutorsAreNeverDeclaredTheOrchestratorsArguments(t *testing.T) {
	for name, spec := range registry.Specs {
		offered := map[string]bool{}
		for _, p := range spec.ModelParams {
			offered[p.Name] = true
		}
		for _, p := range spec.Params {
			if p.Name == registry.ConfirmationParam || p.Name == registry.AcknowledgementParam {
				t.Errorf("%s declares %q to its executor", name, p.Name)
			}
			if !offered[p.Name] {
				t.Errorf("%s does not offer the model its own %q", name, p.Name)
			}
		}
	}
	if spec := registry.Specs["media_search"]; len(spec.ModelParams) != len(spec.Params)+1 {
		t.Errorf("media_search offers %d parameters for %d declared, want one more: the acknowledgement", len(spec.ModelParams), len(spec.Params))
	}
}
