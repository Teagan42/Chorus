package registry_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/registry"
)

// declared strips a generated spec back to what its CUE declares, as a
// provider hands one over: no model view yet.
func declared(spec registry.ToolSpec) registry.ToolSpec {
	spec.ModelDescription, spec.ModelParams = "", nil
	return spec
}

// A tool a provider declares at runtime is shown to the model the way
// schemagen shows a declared one: finishing every generated tool from its
// declaration gives back exactly what was generated, so the two cannot drift.
//
// verifies SPEC §6
func TestAProvidedToolIsOfferedAsAGeneratedOneIs(t *testing.T) {
	for name, spec := range registry.Specs {
		got, err := registry.Provide(declared(spec))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !reflect.DeepEqual(got, spec) {
			t.Errorf("%s: Provide = %+v\nwant the generated %+v", name, got, spec)
		}
	}
}

// forecast is the weather server's tool as the household's MCP config would
// leave it: held for a yes, for identified people, cancelled on a barge-in.
func forecast() registry.ToolSpec {
	return registry.ToolSpec{
		Name:        "mcp_weather_get_forecast",
		Description: "Get the forecast for a place.",
		Params: []registry.ParamSpec{
			{Name: "city", Type: "string", Description: "City name.", Required: true},
			{Name: "days", Type: "integer", Description: "How many days."},
		},
		OnInterrupt:          registry.InterruptCancel,
		Scope:                registry.ScopePerson,
		UnknownSpeaker:       "deny",
		Timeout:              5 * time.Second,
		RequiresConfirmation: true,
	}
}

// A confirmable provided tool carries the nonce and says how to finish a
// held call; a slow one asks for what to say while it works.
//
// verifies SPEC §6
func TestAProvidedToolCarriesTheOrchestratorsArguments(t *testing.T) {
	got, err := registry.Provide(forecast())
	if err != nil {
		t.Fatalf("Provide: %v", err)
	}
	names := func(ps []registry.ParamSpec) (out []string) {
		for _, p := range ps {
			out = append(out, p.Name)
		}
		return out
	}
	if want := []string{"city", "days", registry.ConfirmationParam}; !reflect.DeepEqual(names(got.ModelParams), want) {
		t.Errorf("model params = %q, want %q", names(got.ModelParams), want)
	}
	if !strings.Contains(got.ModelDescription, "Every call needs the person's yes") {
		t.Errorf("model description = %q, want it to say every call is held", got.ModelDescription)
	}
	if len(got.Params) != 2 {
		t.Errorf("params = %v, want the executor's own two untouched", got.Params)
	}

	slow := forecast()
	slow.Slow, slow.Timeout = true, 20*time.Second
	got, err = registry.Provide(slow)
	if err != nil {
		t.Fatalf("Provide slow: %v", err)
	}
	if want := []string{"city", "days", registry.AcknowledgementParam, registry.ConfirmationParam}; !reflect.DeepEqual(names(got.ModelParams), want) {
		t.Errorf("slow model params = %q, want %q", names(got.ModelParams), want)
	}
}

// What tool.cue refuses at build time, Provide refuses at runtime: a
// provider's tool is held to the same policy as a declared one.
//
// verifies SPEC §6
func TestAProvidedToolIsHeldToTheRegistrysRules(t *testing.T) {
	cases := []struct {
		why    string
		change func(*registry.ToolSpec)
		want   string
	}{
		{"a name the wire cannot carry", func(s *registry.ToolSpec) { s.Name = "mcp_weather_Get-Forecast" }, "name"},
		{"no description", func(s *registry.ToolSpec) { s.Description = "" }, "description"},
		{"the nonce's name", func(s *registry.ToolSpec) {
			s.Params = append(s.Params, registry.ParamSpec{Name: "confirmation", Type: "string", Description: "x"})
		}, "confirmation"},
		{"the acknowledgement's name", func(s *registry.ToolSpec) {
			s.Params = append(s.Params, registry.ParamSpec{Name: "acknowledgement", Type: "string", Description: "x"})
		}, "acknowledgement"},
		{"a parameter twice", func(s *registry.ToolSpec) { s.Params = append(s.Params, s.Params[0]) }, "twice"},
		{"a type JSON Schema lacks", func(s *registry.ToolSpec) { s.Params[1].Type = "date" }, "type"},
		{"an array of nothing", func(s *registry.ToolSpec) { s.Params[1].Type = "array" }, "items"},
		{"a parameter with no description", func(s *registry.ToolSpec) { s.Params[0].Description = "" }, "description"},
		{"a timeout under 100 ms", func(s *registry.ToolSpec) { s.Timeout = 50 * time.Millisecond }, "timeout"},
		{"a timeout over two minutes", func(s *registry.ToolSpec) { s.Timeout = 3 * time.Minute }, "timeout"},
		{"an unknown policy", func(s *registry.ToolSpec) { s.OnInterrupt = "abandon" }, "on_interrupt"},
		{"an unknown scope", func(s *registry.ToolSpec) { s.Scope = "family" }, "scope"},
		{"uninterruptible without a yes", func(s *registry.ToolSpec) {
			s.OnInterrupt, s.RequiresConfirmation = registry.InterruptUninterruptible, false
		}, "uninterruptible"},
		{"a person tool silent on strangers", func(s *registry.ToolSpec) { s.UnknownSpeaker = "" }, "unknown_speaker"},
		{"a household tool that names strangers", func(s *registry.ToolSpec) { s.Scope = registry.ScopeHousehold }, "unknown_speaker"},
		{"slow inside the default timeout", func(s *registry.ToolSpec) { s.Slow = true }, "slow"},
		{"an empty confirm_when entry", func(s *registry.ToolSpec) {
			s.ConfirmWhen = []registry.ConfirmRule{{}}
		}, "confirm_when"},
	}
	for _, c := range cases {
		spec := forecast()
		c.change(&spec)
		_, err := registry.Provide(spec)
		if err == nil {
			t.Errorf("%s: Provide accepted it", c.why)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %q, want it to name %q", c.why, err, c.want)
		}
	}
}

// A provided tool's arguments reach its server as written, so they keep the
// server's own names, camelCase included; the tool's own name is still the
// registry's.
//
// verifies SPEC §6
func TestAProvidedToolKeepsItsServersParameterNames(t *testing.T) {
	spec := forecast()
	spec.Params = append(spec.Params,
		registry.ParamSpec{Name: "unitSystem", Type: "string", Description: "metric or imperial."},
		registry.ParamSpec{Name: "max-results", Type: "integer", Description: "At most this many."},
	)
	if _, err := registry.Provide(spec); err != nil {
		t.Errorf("Provide: %v", err)
	}
	spec.Params = append(spec.Params, registry.ParamSpec{Name: "has space", Type: "string", Description: "x"})
	if _, err := registry.Provide(spec); err == nil {
		t.Error("Provide accepted a parameter name with a space in it")
	}
}
