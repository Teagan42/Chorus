package mcp_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/provider/mcp"
	"github.com/teagan42/chorus/internal/registry"
)

// A server's tool is called mcp_<server>_<tool>, in the registry's alphabet,
// so it can never take a native or Home Assistant tool's name.
//
// verifies SPEC §6
func TestAServersToolIsNamedUnderItsServer(t *testing.T) {
	cases := map[string]string{
		"get_forecast": "mcp_weather_get_forecast",
		"getForecast":  "mcp_weather_getforecast",
		"get-forecast": "mcp_weather_get_forecast",
		"radar.png":    "mcp_weather_radar_png",
		"météo":        "mcp_weather_m_t_o",
	}
	for tool, want := range cases {
		got, err := mcp.Name("weather", tool)
		if err != nil || got != want {
			t.Errorf("Name(weather, %q) = %q, %v; want %q", tool, got, err, want)
		}
	}
	if _, err := mcp.Name("weather", strings.Repeat("forecast_", 8)); err == nil {
		t.Error("a name past 64 characters was accepted")
	}
	if _, err := mcp.Name("weather", ""); err == nil {
		t.Error("a tool with no name was accepted")
	}
}

func params(t *testing.T, schema string) []registry.ParamSpec {
	t.Helper()
	got, err := mcp.Params(json.RawMessage(schema))
	if err != nil {
		t.Fatalf("Params: %v", err)
	}
	return got
}

// The forecast's schema maps onto the registry's parameters one for one:
// required first in the schema's order, the rest by name, string enums
// kept, an array's item type kept.
//
// verifies SPEC §6
func TestAPlainSchemaMapsOntoTheRegistrysParameters(t *testing.T) {
	got := params(t, `{
		"type": "object",
		"properties": {
			"units": {"type": "string", "enum": ["metric", "imperial"], "description": "Unit system."},
			"days": {"type": "integer", "description": "How many days ahead."},
			"city": {"type": "string", "description": "City name."},
			"country": {"type": "string", "title": "Country code"},
			"fields": {"type": "array", "items": {"type": "string"}, "description": "Which fields."},
			"hourly": {"type": "boolean"}
		},
		"required": ["days", "city", "nowhere"]
	}`)
	want := []registry.ParamSpec{
		{Name: "days", Type: "integer", Description: "How many days ahead.", Required: true},
		{Name: "city", Type: "string", Description: "City name.", Required: true},
		{Name: "country", Type: "string", Description: "Country code"},
		{Name: "fields", Type: "array", Description: "Which fields.", Items: "string"},
		{Name: "hourly", Type: "boolean", Description: "The hourly."},
		{Name: "units", Type: "string", Description: "Unit system.", Enum: []string{"metric", "imperial"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Params =\n%+v\nwant\n%+v", got, want)
	}
}

// What the wire cannot carry (a nested object's fields, a union, a number's
// range) stays in the parameter's description as JSON, so the model is told
// what the server will check.
//
// verifies SPEC §6
func TestASchemaTheWireCannotCarryIsDescribed(t *testing.T) {
	got := params(t, `{
		"type": "object",
		"properties": {
			"location": {"type": "object", "description": "Where.", "properties": {"lat": {"type": "number"}, "lon": {"type": "number"}}, "required": ["lat", "lon"]},
			"days": {"type": "integer", "description": "Days.", "minimum": 1, "maximum": 7},
			"when": {"anyOf": [{"type": "string", "format": "date"}, {"type": "null"}], "description": "Day."},
			"note": {"type": ["string", "null"], "description": "A note."},
			"stops": {"type": "array", "items": {"type": "object", "properties": {"city": {"type": "string"}}}, "description": "Stops."},
			"level": {"type": "integer", "enum": [1, 2, 3], "description": "Level."}
		}
	}`)
	by := map[string]registry.ParamSpec{}
	for _, p := range got {
		by[p.Name] = p
	}
	cases := []struct {
		name, typ, items, description string
	}{
		{"location", "object", "", `Where. Schema: {"properties":{"lat":{"type":"number"},"lon":{"type":"number"}},"required":["lat","lon"],"type":"object"}`},
		{"days", "integer", "", `Days. Schema: {"maximum":7,"minimum":1,"type":"integer"}`},
		{"when", "string", "", `Day. Schema: {"anyOf":[{"format":"date","type":"string"},{"type":"null"}]}`},
		{"note", "string", "", "A note."},
		{"stops", "array", "object", `Stops. Schema: {"items":{"properties":{"city":{"type":"string"}},"type":"object"},"type":"array"}`},
		{"level", "integer", "", `Level. Schema: {"enum":[1,2,3],"type":"integer"}`},
	}
	for _, c := range cases {
		p := by[c.name]
		if p.Type != c.typ || p.Items != c.items || p.Description != c.description || p.Enum != nil {
			t.Errorf("%s = %+v\nwant type %s, items %q, description %s", c.name, p, c.typ, c.items, c.description)
		}
	}
}

// A tool that takes nothing has no parameters; a schema that is not an
// object is refused, and a parameter's prose is bounded.
//
// verifies SPEC §6
func TestASchemasEdgesAreHeld(t *testing.T) {
	if got := params(t, `{"type":"object"}`); got != nil {
		t.Errorf("no properties: Params = %v, want none", got)
	}
	if _, err := mcp.Params(json.RawMessage(`{"type":"array"}`)); err == nil {
		t.Error("an array input schema was accepted")
	}
	long := strings.Repeat("é", 2000)
	got := params(t, `{"type":"object","properties":{"city":{"type":"string","description":"`+long+`"}}}`)
	if d := got[0].Description; len(d) > 1024 || !strings.HasSuffix(d, "…") || !strings.HasPrefix(d, "éé") {
		t.Errorf("long description is %d bytes, ending %q", len(d), d[len(d)-6:])
	}
}
