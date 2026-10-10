package ollama

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// contactSensor is the reviewer's rewording after the garage turn read the
// cover nobody can reach: name the sensor that answers.
const contactSensor = "Read one entity's current state. For a door or cover, read its contact sensor, binary_sensor.<name>_contact."

// editSchema parses the registry's schema, lets edit change the tools, and
// writes it back the way Replay's textarea holds it.
func editSchema(t *testing.T, edit func([]map[string]any) []map[string]any) string {
	t.Helper()
	var tools []map[string]any
	if err := json.Unmarshal([]byte(ToolSchema(registry.Specs)), &tools); err != nil {
		t.Fatalf("the registry's schema is not JSON: %v", err)
	}
	b, err := json.MarshalIndent(edit(tools), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An unedited schema read back is the schema the turn ran under, so a re-run
// that only changes the prompt keeps the recorded tool-schema version.
//
// verifies SPEC §8, §9.2
func TestTheRegistrysToolSchemaReadsBackToItsOwnVersion(t *testing.T) {
	specs, err := ParseToolSchema(ToolSchema(registry.Specs))
	if err != nil {
		t.Fatalf("parse the registry's own schema: %v", err)
	}
	want := engineOn(t, &roundTrip{}, Config{}).Versions()
	if got := engineOn(t, &roundTrip{}, Config{Specs: specs}).Versions(); got != want {
		t.Errorf("versions = %+v, want the registry's %+v", got, want)
	}
	if ToolSchema(specs) != ToolSchema(registry.Specs) {
		t.Error("the schema read back does not write out as it was")
	}
}

// The editor shows a description as the reviewer typed it: binary_sensor.<name>
// is not written <name> back at them.
//
// verifies SPEC §9.2
func TestTheToolSchemaReadsAsWritten(t *testing.T) {
	specs := map[string]registry.ToolSpec{"ha_get_state": {Name: "ha_get_state", ModelDescription: contactSensor}}
	got := ToolSchema(specs)
	if !strings.Contains(got, "binary_sensor.<name>_contact") || strings.HasSuffix(got, "\n") {
		t.Errorf("the schema reads %q", got)
	}
}

// The reviewer drops media_search and rewords ha_get_state: the model is
// offered exactly that, and the tool-schema version moves with it.
//
// verifies SPEC §9.2
func TestAnEditedToolSchemaIsWhatTheModelIsOffered(t *testing.T) {
	edited := editSchema(t, func(tools []map[string]any) []map[string]any {
		var out []map[string]any
		for _, tool := range tools {
			fn := tool["function"].(map[string]any)
			switch fn["name"] {
			case "media_search":
				continue
			case "ha_get_state":
				fn["description"] = contactSensor
			}
			out = append(out, tool)
		}
		return out
	})
	specs, err := ParseToolSchema(edited)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rt := &roundTrip{body: fixture(t, "reply.ndjson")}
	e := engineOn(t, rt, Config{Specs: specs})
	ch, err := e.Turn(context.Background(), session.Input{Speaker: "teagan", Room: "office", Text: "is the garage door closed"})
	if err != nil {
		t.Fatalf("turn: %v", err)
	}
	drain(t, ch)

	var sent struct {
		Tools []wireTool `json:"tools"`
	}
	if err := json.Unmarshal(rt.reqBody, &sent); err != nil {
		t.Fatalf("request body is not JSON: %v", err)
	}
	offered := map[string]wireFunction{}
	for _, tool := range sent.Tools {
		offered[tool.Function.Name] = tool.Function
	}
	if _, ok := offered["media_search"]; ok {
		t.Error("the dropped media_search is still offered")
	}
	if got := offered["ha_get_state"]; got.Description != contactSensor || got.Parameters.Required[0] != "entity_id" {
		t.Errorf("ha_get_state is offered as %+v", got)
	}
	if len(offered) != len(registry.Specs)-1 {
		t.Errorf("%d tools offered, want every other one", len(offered))
	}

	recorded := engineOn(t, &roundTrip{}, Config{}).Versions()
	if v := e.Versions(); v.ToolSchema == recorded.ToolSchema || v.Prompt != recorded.Prompt {
		t.Errorf("versions %+v against %+v: want a new tool schema, the same prompt", v, recorded)
	}
}

// A schema the wire cannot carry is refused, saying where, rather than
// quietly offered as something else.
//
// verifies SPEC §9.2
func TestAMalformedToolSchemaIsRefusedSayingWhy(t *testing.T) {
	getState := func(params string) string {
		return `[{"type": "function", "function": {"name": "ha_get_state", "description": "Read one entity.", "parameters": ` + params + `}}]`
	}
	for name, c := range map[string]struct{ schema, want string }{
		"cut short":         {"[\n  {\"type\": \"function\",\n  \"function\": {\"name\": \"ha_get_state\"", "ends before it is closed"},
		"a stray comma":     {"[\n  {\"type\": \"function\",\n   \"function\": {\"name\": \"ha_get_state\",}}]", "line 3"},
		"not a list":        {`{"type": "function"}`, "a JSON array of tools"},
		"a name that is 7":  {`[{"type": "function", "function": {"name": 7}}]`, "function.name cannot be a JSON number"},
		"more after it":     {getState(`{"type": "object", "properties": {}}`) + ` []`, "after the closing ]"},
		"a field it drops":  {getState(`{"type": "object", "properties": {"entity_id": {"type": "string", "minimum": 1}}}`), `unknown field "minimum"`},
		"no name":           {`[{"type": "function", "function": {"description": "Read one entity.", "parameters": {"type": "object"}}}]`, "tool 1 has no name"},
		"not a function":    {`[{"type": "retrieval", "function": {"name": "ha_get_state", "parameters": {"type": "object"}}}]`, `ha_get_state has type "retrieval"`},
		"declared twice":    {strings.Replace(getState(`{"type": "object"}`), "}}]", "}}, "+getState(`{"type": "object"}`)[1:], 1), "ha_get_state is declared twice"},
		"no object schema":  {getState(`{"type": "string"}`), `ha_get_state's parameters have type "string"`},
		"a made-up type":    {getState(`{"type": "object", "properties": {"entity_id": {"type": "entity"}}}`), `ha_get_state.entity_id has type "entity"`},
		"requires a ghost":  {getState(`{"type": "object", "properties": {"entity_id": {"type": "string"}}, "required": ["entity"]}`), `ha_get_state requires "entity", which it does not declare`},
		"items on a string": {getState(`{"type": "object", "properties": {"entity_id": {"type": "string", "items": {"type": "string"}}}}`), "ha_get_state.entity_id has items but is not an array"},
	} {
		_, err := ParseToolSchema(c.schema)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
}
