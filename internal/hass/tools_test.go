package hass_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/registry"
)

// The CUE declaration and the Go implementation are two files that name the
// same tools. This is the only thing that stops them drifting (SPEC §6).
func TestEveryDeclaredHAToolIsImplementedAndNothingElse(t *testing.T) {
	tools := hass.Tools(clientOn(t, newTransport(0, "")))

	for name := range registry.Specs {
		if !strings.HasPrefix(name, "ha_") {
			continue
		}
		if _, ok := tools[name]; !ok {
			t.Errorf("%s is declared in schema/tool.cue but hass.Tools does not implement it", name)
		}
	}
	for name := range tools {
		if !strings.HasPrefix(name, "ha_") {
			t.Errorf("%s is not an ha_ tool", name)
		}
		if _, ok := registry.Specs[name]; !ok {
			t.Errorf("%s is implemented but not declared in schema/tool.cue", name)
		}
	}
}

// sample is a well-typed value for a declared parameter, so every declared
// field can be sent to every tool.
func sample(p registry.ParamSpec) any {
	switch p.Name {
	case "domain":
		return "light"
	case "service":
		return "turn_on"
	case "entity_id":
		return "light.kitchen"
	case "area_id":
		return "kitchen"
	}
	switch p.Type {
	case "string":
		return "kitchen"
	case "integer", "number":
		return 1
	case "boolean":
		return true
	case "array":
		return []any{}
	default:
		return map[string]any{}
	}
}

// A handler that reads a parameter under a name the schema does not declare
// would silently read nothing. Sending every declared field proves each
// handler's reads line up with the declaration.
func TestHandlersReadOnlyDeclaredParameters(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	tr.routes = map[string]string{"/api/states/light.kitchen": kitchenOn[1 : len(kitchenOn)-1]}
	tools := hass.Tools(clientOn(t, tr))

	for name, tool := range tools {
		args := map[string]any{}
		for _, p := range registry.Specs[name].Params {
			args[p.Name] = sample(p)
		}
		raw, _ := json.Marshal(args)
		if _, err := tool.Invoke(context.Background(), string(raw)); err != nil {
			t.Errorf("%s with every declared field: %v", name, err)
		}
	}
}

func TestCallServiceReturnsWhatChanged(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	tools := hass.Tools(clientOn(t, tr))

	out, err := tools["ha_call_service"].Invoke(context.Background(),
		`{"domain":"light","service":"turn_on","entity_id":"light.kitchen","data":{"brightness_pct":50}}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if want := `{"changed":[{"entity_id":"light.kitchen","state":"on"}]}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	req := tr.last(t)
	if req.path != "/api/services/light/turn_on" {
		t.Errorf("path = %q", req.path)
	}
	if req.body != `{"brightness_pct":50,"entity_id":"light.kitchen"}` {
		t.Errorf("body = %s, want the target beside the service data", req.body)
	}
}

// An area is a legitimate target, and a call that changed nothing is an
// answer the model has to hear as such, not as a failure.
func TestCallServiceByAreaReportsAnEmptyChange(t *testing.T) {
	tr := newTransport(http.StatusOK, `[]`)
	tools := hass.Tools(clientOn(t, tr))

	out, err := tools["ha_call_service"].Invoke(context.Background(),
		`{"domain":"light","service":"turn_off","area_id":"kitchen"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if out != `{"changed":[]}` {
		t.Errorf("result = %s", out)
	}
	if req := tr.last(t); req.body != `{"area_id":"kitchen"}` {
		t.Errorf("body = %s", req.body)
	}
}

// The declaration is what the model sees; the implementation accepts exactly
// that, so an argument the model invents or omits is a result, not a 400
// from HA that names a field the model never saw.
func TestArgumentsAreCheckedAgainstTheDeclaration(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	tools := hass.Tools(clientOn(t, tr))

	cases := []struct {
		tool, args, want string
	}{
		{"ha_call_service", `{"domain":"light","service":"turn_on","entity_id":"light.kitchen","colour":"red"}`, `"colour"`},
		{"ha_call_service", `{"service":"turn_on","entity_id":"light.kitchen"}`, `"domain"`},
		{"ha_call_service", `{"domain":"light","service":"turn_on"}`, "entity_id or area_id"},
		{"ha_call_service", `{"domain":5,"service":"turn_on","entity_id":"light.kitchen"}`, `"domain"`},
		{"ha_call_service", `{"domain":"light/turn_on","service":"x","entity_id":"light.kitchen"}`, `"domain"`},
		{"ha_call_service", `{"domain":"light","service":"turn_on","entity_id":"kitchen"}`, `"entity_id"`},
		{"ha_call_service", `{"domain":"light","service":"turn_on","entity_id":"light.kitchen","data":"bright"}`, `"data"`},
		{"ha_get_state", `{}`, `"entity_id"`},
		{"ha_get_state", `not json`, "bad arguments"},
		{"ha_find_entities", `{"room":"kitchen"}`, `"room"`},
	}
	for _, c := range cases {
		_, err := tools[c.tool].Invoke(context.Background(), c.args)
		if err == nil {
			t.Errorf("%s %s: accepted", c.tool, c.args)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %s: err = %v, want it to name %s", c.tool, c.args, err, c.want)
		}
	}
	if n := tr.count(); n != 0 {
		t.Errorf("%d requests reached HA for arguments that should have been rejected first", n)
	}
}

// Absent and null are the same thing to a model that fills every slot.
func TestNullArgumentsAreAbsent(t *testing.T) {
	tr := newTransport(http.StatusOK, kitchenOn)
	tools := hass.Tools(clientOn(t, tr))
	_, err := tools["ha_call_service"].Invoke(context.Background(),
		`{"domain":"light","service":"turn_on","entity_id":"light.kitchen","area_id":null,"data":null}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if req := tr.last(t); req.body != `{"entity_id":"light.kitchen"}` {
		t.Errorf("body = %s", req.body)
	}
}

// Every HA failure is an error the session turns into a result the model
// reasons about; none of them is a crash (SPEC §7).
func TestHAFailuresAreErrorsTheModelCanRead(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		tool   string
		args   string
		want   string
		is     error
	}{
		{
			"rejected token", http.StatusUnauthorized, `{"message":"Unauthorized"}`,
			"ha_call_service", `{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}`,
			"rejected the access token", hass.ErrUnauthorized,
		},
		{
			"unknown entity", http.StatusNotFound, `{"message":"Entity not found."}`,
			"ha_get_state", `{"entity_id":"light.nope"}`,
			"unknown entity light.nope", hass.ErrUnknownEntity,
		},
		{
			"unknown service", http.StatusBadRequest, `{"message":"Service light.explode not found."}`,
			"ha_call_service", `{"domain":"light","service":"explode","entity_id":"light.kitchen"}`,
			"call light.explode: 400 Bad Request: {\"message\":\"Service light.explode not found.\"}", nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tools := hass.Tools(clientOn(t, newTransport(c.status, c.body)))
			_, err := tools[c.tool].Invoke(context.Background(), c.args)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
			if c.is != nil && !errors.Is(err, c.is) {
				t.Errorf("err = %v, want errors.Is %v", err, c.is)
			}
			if strings.Contains(err.Error(), token) {
				t.Errorf("err quotes the token: %v", err)
			}
		})
	}
}

// Attributes carry everything from a friendly name to a 48-hour forecast.
// Scalars are what a voice answer is made of; the rest is noise in the prompt.
func TestGetStateKeepsScalarAttributesOnly(t *testing.T) {
	body := `{"entity_id":"climate.hall","state":"heat","attributes":{"friendly_name":"Hall","temperature":21.5,"hvac_modes":["off","heat"],"preset":null,"nested":{"a":1},"on":true},"last_changed":"2026-10-08T09:00:00+00:00"}`
	tools := hass.Tools(clientOn(t, newTransport(http.StatusOK, body)))

	out, err := tools["ha_get_state"].Invoke(context.Background(), `{"entity_id":"climate.hall"}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	want := `{"entity_id":"climate.hall","state":"heat","attributes":{"friendly_name":"Hall","on":true,"temperature":21.5},"last_changed":"2026-10-08T09:00:00+00:00"}`
	if out != want {
		t.Errorf("result = %s\n  want = %s", out, want)
	}
}

func states(n int) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range n {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"entity_id":"light.lamp_%02d","state":"off","attributes":{"friendly_name":"Lamp %d"}}`, i, i)
	}
	b.WriteString("]")
	return b.String()
}

const household = `[
 {"entity_id":"light.kitchen_ceiling","state":"on","attributes":{"friendly_name":"Kitchen Ceiling"}},
 {"entity_id":"switch.kitchen_kettle","state":"off","attributes":{"friendly_name":"Kettle"}},
 {"entity_id":"light.office","state":"off","attributes":{"friendly_name":"Office Lamp"}},
 {"entity_id":"sensor.outside","state":"12.5","attributes":{}}
]`

// Domain is an exact match on the id's prefix; name is a substring of the
// friendly name or the id, case-folded, since the model has heard a room
// name, not an id.
func TestFindEntitiesFiltersByDomainAndName(t *testing.T) {
	tr := newTransport(http.StatusOK, household)
	tools := hass.Tools(clientOn(t, tr))
	find := func(args string) string {
		t.Helper()
		out, err := tools["ha_find_entities"].Invoke(context.Background(), args)
		if err != nil {
			t.Fatalf("find %s: %v", args, err)
		}
		return out
	}

	if got := find(`{"domain":"light","name":"KITCHEN"}`); got != `{"entities":[{"entity_id":"light.kitchen_ceiling","name":"Kitchen Ceiling","state":"on"}]}` {
		t.Errorf("by domain and name = %s", got)
	}
	// The id matches too: "kettle" is in the id, not only the friendly name.
	if got := find(`{"name":"kitchen"}`); got != `{"entities":[{"entity_id":"light.kitchen_ceiling","name":"Kitchen Ceiling","state":"on"},{"entity_id":"switch.kitchen_kettle","name":"Kettle","state":"off"}]}` {
		t.Errorf("by name = %s", got)
	}
	// No friendly name falls back to the id rather than an empty string.
	if got := find(`{"domain":"sensor"}`); got != `{"entities":[{"entity_id":"sensor.outside","name":"sensor.outside","state":"12.5"}]}` {
		t.Errorf("by domain = %s", got)
	}
	if got := find(`{"name":"garage"}`); got != `{"entities":[]}` {
		t.Errorf("no match = %s", got)
	}
	if req := tr.last(t); req.method != http.MethodGet || req.path != "/api/states" {
		t.Errorf("request = %s %s", req.method, req.path)
	}
}

// An unfiltered call on a real home is hundreds of entities. The model gets a
// page and is told there is more, rather than a prompt-sized dump.
func TestFindEntitiesIsBounded(t *testing.T) {
	tools := hass.Tools(clientOn(t, newTransport(http.StatusOK, states(hass.MaxEntities+5))))
	out, err := tools["ha_find_entities"].Invoke(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	var got struct {
		Entities  []map[string]string `json:"entities"`
		Truncated bool                `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if len(got.Entities) != hass.MaxEntities || !got.Truncated {
		t.Errorf("got %d entities, truncated=%v; want %d and truncated", len(got.Entities), got.Truncated, hass.MaxEntities)
	}
	// Sorted, so the same home always pages the same way.
	if got.Entities[0]["entity_id"] != "light.lamp_00" {
		t.Errorf("first = %v, want light.lamp_00", got.Entities[0])
	}
}
