package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teagan42/chorus/internal/provider/mcp"
	"github.com/teagan42/chorus/internal/registry"
)

const weatherOnly = `
servers:
  weather:
    url: http://weather.lan:8080/mcp
`

// The weather server's forecast is offered under every conservative default
// SPEC §6 asks of an MCP tool: a namespaced name, identified people only,
// every call held for a yes, cancelled on a barge-in, five seconds.
//
// verifies SPEC §6
func TestTheWeatherForecastIsOfferedUnderConservativePolicy(t *testing.T) {
	p := start(t, configured(t, weatherOnly), houses{"weather": weather()}, &clock{}, &logBuffer{})
	specs := p.Specs()
	spec, ok := specs["mcp_weather_get_forecast"]
	if !ok || len(specs) != 1 {
		t.Fatalf("specs = %v, want mcp_weather_get_forecast alone", specs)
	}
	want := struct {
		scope   registry.Scope
		unknown string
		confirm bool
		policy  registry.InterruptPolicy
		timeout time.Duration
		slow    bool
	}{registry.ScopePerson, "deny", true, registry.InterruptCancel, 5 * time.Second, false}
	got := want
	got.scope, got.unknown, got.confirm = spec.Scope, spec.UnknownSpeaker, spec.RequiresConfirmation
	got.policy, got.timeout, got.slow = spec.OnInterrupt, spec.Timeout, spec.Slow
	if got != want {
		t.Errorf("policy = %+v, want %+v", got, want)
	}
	if spec.Description != "Get the weather forecast for a city." {
		t.Errorf("description = %q, want the server's own", spec.Description)
	}
	var params []string
	for _, p := range spec.ModelParams {
		params = append(params, p.Name)
	}
	if want := []string{"city", "days", registry.ConfirmationParam}; !reflect.DeepEqual(params, want) {
		t.Errorf("model params = %q, want %q", params, want)
	}
	if _, ok := p.Tools()["mcp_weather_get_forecast"]; !ok || len(p.Tools()) != 1 {
		t.Errorf("tools = %v, want the forecast's executor alone", p.Tools())
	}
}

// Teagan asks for Lisbon's weather: the call reaches the server with the
// arguments the model wrote, and its text comes back as the result.
//
// verifies SPEC §6
func TestAForecastCallReachesTheServerAndComesBack(t *testing.T) {
	h := weather()
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, &logBuffer{})
	out, err := p.Tools()["mcp_weather_get_forecast"].Invoke(context.Background(), `{"city":"Lisbon","days":1}`)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if want := `{"text":"Sunny and 21°C in Lisbon."}`; out != want {
		t.Errorf("result = %s, want %s", out, want)
	}
	if got := h.calls(); len(got) != 1 || got[0] != `get_forecast {"city":"Lisbon","days":1}` {
		t.Errorf("server was called with %q", got)
	}
}

// A server's result says what it can: structured content as it is, text
// joined, and what the model cannot read (a radar image) named, not inlined.
//
// verifies SPEC §6
func TestAResultIsWhatTheModelCanRead(t *testing.T) {
	h := newHouse("weather")
	h.tool(&sdk.Tool{Name: "get_conditions", Description: "Current conditions."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{
			Content:           []sdk.Content{&sdk.TextContent{Text: `{"temp_c":21}`}},
			StructuredContent: map[string]any{"temp_c": 21, "sky": "clear"},
		}, nil
	})
	h.tool(&sdk.Tool{Name: "get_radar", Description: "The rain radar."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{
			&sdk.TextContent{Text: "Light rain to the west."},
			&sdk.ImageContent{MIMEType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
		}}, nil
	})
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, &logBuffer{})
	tools := p.Tools()
	cases := map[string]string{
		"mcp_weather_get_conditions": `{"sky":"clear","temp_c":21}`,
		"mcp_weather_get_radar":      `{"text":"Light rain to the west.","omitted":["image image/png"]}`,
	}
	for name, want := range cases {
		out, err := tools[name].Invoke(context.Background(), "")
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if out != want {
			t.Errorf("%s = %s, want %s", name, out, want)
		}
	}
}

// A city the server does not know is the tool's error, and a call the
// server refuses outright is the server's: both are errors the session
// records as results the model reasons about, never a hang (SPEC §7).
//
// verifies SPEC §6, §7
func TestAToolsErrorIsAResultTheModelReads(t *testing.T) {
	h := newHouse("weather")
	h.tool(&sdk.Tool{Name: "get_forecast", Description: "Forecast."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "City not found: Atlantis"}}}, nil
	})
	h.tool(&sdk.Tool{Name: "get_alerts", Description: "Weather alerts."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return nil, errors.New("alerts feed is down")
	})
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, &logBuffer{})
	tools := p.Tools()
	cases := map[string]string{
		"mcp_weather_get_forecast": "tool_error: City not found: Atlantis",
		"mcp_weather_get_alerts":   "mcp_error: the weather server refused get_alerts: alerts feed is down",
	}
	for name, want := range cases {
		_, err := tools[name].Invoke(context.Background(), `{"city":"Atlantis"}`)
		if err == nil || err.Error() != want {
			t.Errorf("%s: error %v, want %q", name, err, want)
		}
	}
	if _, err := tools["mcp_weather_get_forecast"].Invoke(context.Background(), `{"city":`); err == nil || !strings.HasPrefix(err.Error(), "bad_arguments") {
		t.Errorf("broken JSON: error %v, want bad_arguments", err)
	}
}

// slowHouse is a server whose forecast waits until it is cancelled or let
// go, reporting each cancellation it saw.
func slowHouse() (h *house, release chan struct{}, cancelled *atomic.Int32) {
	h = newHouse("weather")
	release, cancelled = make(chan struct{}), &atomic.Int32{}
	h.tool(&sdk.Tool{Name: "get_forecast", Description: "Forecast, slowly."}, func(ctx context.Context, _ map[string]any) (*sdk.CallToolResult, error) {
		select {
		case <-release:
			return text("Fog."), nil
		case <-ctx.Done():
			cancelled.Add(1)
			return nil, ctx.Err()
		}
	})
	return h, release, cancelled
}

// Alan barges in on a forecast. The session cancels the call's context,
// as the default on_interrupt says; the call returns at once, cancelled,
// and the server is told to stop too, so nothing is left running.
//
// verifies SPEC §4.4, §6
func TestACancelledCallStopsOnTheServerToo(t *testing.T) {
	h, _, cancelled := slowHouse()
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, &logBuffer{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.Tools()["mcp_weather_get_forecast"].Invoke(ctx, `{}`)
		done <- err
	}()
	await(t, "the call to reach the server", func() bool { return len(h.calls()) == 1 })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error %v, want context.Canceled, which the session records as cancelled", err)
		}
	case <-time.After(patience):
		t.Fatal("the cancelled call did not return")
	}
	await(t, "the server to see the call cancelled", func() bool { return cancelled.Load() == 1 })
}

// The weather server crashes while it works on a forecast. The call answers
// mcp_unavailable rather than hanging; the provider reconnects after its
// backoff, and the next forecast works.
//
// verifies SPEC §6, §7
func TestAServerThatDiesMidCallAnswersAndIsReconnected(t *testing.T) {
	h, release, _ := slowHouse()
	clk, logs := &clock{}, &logBuffer{}
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, clk, logs)
	forecast := p.Tools()["mcp_weather_get_forecast"]
	done := make(chan error, 1)
	go func() {
		_, err := forecast.Invoke(context.Background(), `{}`)
		done <- err
	}()
	await(t, "the call to reach the server", func() bool { return len(h.calls()) == 1 })
	h.crash()
	select {
	case err := <-done:
		if err == nil || !strings.HasPrefix(err.Error(), "mcp_unavailable: ") {
			t.Errorf("error %v, want mcp_unavailable", err)
		}
	case <-time.After(patience):
		t.Fatal("the call outlived its server")
	}
	logs.await(t, "mcp server connection lost")
	if _, err := forecast.Invoke(context.Background(), `{}`); err == nil || err.Error() != "mcp_unavailable: the weather server is not connected" {
		t.Errorf("while down: error %v, want not connected", err)
	}

	close(release)
	clk.awaitWait(t, time.Second)
	clk.advance(time.Second)
	await(t, "a reconnect", func() bool { return h.dialCount() == 2 })
	logs.await(t, "mcp server connected")
	await(t, "the forecast to work again", func() bool {
		out, err := forecast.Invoke(context.Background(), `{}`)
		return err == nil && out == `{"text":"Fog."}`
	})
}

// The weather server is still starting when chorusd does. Startup waits
// its discovery window, then goes on without the server's tools, retrying
// on a doubling backoff; once it answers, the log says a restart offers
// its tools, and the model's offer does not change underneath the journal.
//
// verifies SPEC §6, §8
func TestAServerUnreachableAtStartupOffersNothingAndIsRetried(t *testing.T) {
	h := weather()
	h.refuse(true)
	clk, logs := &clock{}, &logBuffer{}
	cfg := configured(t, "discovery_timeout_ms: 3000\n"+weatherOnly)
	started := make(chan *mcp.Provider, 1)
	go func() {
		p, err := mcp.Start(context.Background(), cfg, mcp.Options{Timers: clk, Log: logs.logger(), Dial: houses{"weather": h}.dial})
		if err != nil {
			t.Errorf("start: %v", err)
		}
		started <- p
	}()
	await(t, "the first dial", func() bool { return h.dialCount() == 1 })
	clk.awaitWait(t, time.Second)
	clk.advance(time.Second)
	await(t, "a retry", func() bool { return h.dialCount() == 2 })
	clk.awaitWait(t, 2*time.Second)
	clk.advance(2 * time.Second)
	var p *mcp.Provider
	select {
	case p = <-started:
	case <-time.After(patience):
		t.Fatal("startup waited past its discovery window")
	}
	defer p.Close()
	if len(p.Specs()) != 0 || len(p.Tools()) != 0 {
		t.Errorf("offered %v from a server that never answered", p.Specs())
	}
	logs.await(t, "mcp server offers nothing")
	logs.await(t, "mcp server unreachable")

	// The retry the window's end let go has failed too, and waits longer.
	clk.awaitWait(t, 4*time.Second)
	h.refuse(false)
	clk.advance(4 * time.Second)
	logs.await(t, "a restart offers those the configuration allows")
	if len(p.Specs()) != 0 {
		t.Errorf("offer changed after startup: %v", p.Specs())
	}
}

// The weather server drops its forecast and adds alerts mid-evening, and
// says so. A forecast call answers mcp_tool_gone instead of reaching a tool
// that is not there; the offer is unchanged until the next start; and when
// the forecast comes back, it works again.
//
// verifies SPEC §6, §8
func TestAChangedToolListIsReconciledForCallsNotOffers(t *testing.T) {
	h := weather()
	logs := &logBuffer{}
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, logs)
	forecast := p.Tools()["mcp_weather_get_forecast"]

	h.srv.RemoveTools("get_forecast")
	h.tool(&sdk.Tool{Name: "get_alerts", Description: "Weather alerts."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return text("No alerts."), nil
	})
	logs.await(t, "no longer lists tools the model is offered")
	if _, err := forecast.Invoke(context.Background(), `{"city":"Lisbon"}`); err == nil || err.Error() != "mcp_tool_gone: the weather server no longer lists get_forecast" {
		t.Errorf("error %v, want mcp_tool_gone", err)
	}
	if _, ok := p.Specs()["mcp_weather_get_alerts"]; ok || len(p.Specs()) != 1 {
		t.Errorf("specs = %v, want the forecast alone until the next start", p.Specs())
	}

	h.tool(&sdk.Tool{Name: "get_forecast", Description: "Forecast."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return text("Rain."), nil
	})
	await(t, "the forecast to come back", func() bool {
		out, err := forecast.Invoke(context.Background(), `{"city":"Lisbon"}`)
		return err == nil && out == `{"text":"Rain."}`
	})
	if strings.Contains(logs.String(), "will not say when its tools change") {
		t.Errorf("a server that reports its changes is logged as one that will not:\n%s", logs)
	}
}

// The weather server is slow to take up chorusd's subscription to its
// changes, and drops its forecast before it does. Its tools are declared,
// not inferred from a list that is empty for the moment, so the forecast's
// return is still heard, and the call works again.
//
// verifies SPEC §6
func TestAForecastDroppedBeforeTheSubscriptionTakesIsHeardComingBack(t *testing.T) {
	h := weather()
	release := h.holdSubscription()
	logs := &logBuffer{}
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, logs)
	forecast := p.Tools()["mcp_weather_get_forecast"]

	h.srv.RemoveTools("get_forecast")
	release()
	logs.await(t, "no longer lists tools the model is offered")
	h.tool(&sdk.Tool{Name: "get_forecast", Description: "Forecast."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return text("Rain."), nil
	})
	await(t, "the forecast to come back", func() bool {
		out, err := forecast.Invoke(context.Background(), `{"city":"Lisbon"}`)
		return err == nil && out == `{"text":"Rain."}`
	})
}

// A weather server that leaves its tools capability to the SDK, and has
// none at the moment it takes up chorusd's subscription, refuses to say
// when they change. The log says so, rather than chorusd waiting on news
// that will not come.
//
// verifies SPEC §6
func TestAServerThatWillNotReportChangesIsLogged(t *testing.T) {
	h := houseWith("weather", nil)
	h.tool(&sdk.Tool{Name: "get_forecast", Description: "Forecast."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return text("Rain."), nil
	})
	release := h.holdSubscription()
	logs := &logBuffer{}
	start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, logs)

	h.srv.RemoveTools("get_forecast")
	release()
	logs.await(t, "will not say when its tools change")
}

// Two tools the server tells apart only by a dash are one name once made a
// tool name; neither is offered, rather than whichever the server listed
// last. A name chorusd already has is refused too.
//
// verifies SPEC §6
func TestToolsThatCollideOnANameAreNotOffered(t *testing.T) {
	h := newHouse("weather")
	for _, name := range []string{"get-forecast", "get_forecast", "get_alerts", "get_radar"} {
		h.tool(&sdk.Tool{Name: name, Description: "Weather."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
			return text("ok"), nil
		})
	}
	logs := &logBuffer{}
	native := map[string]registry.ToolSpec{"mcp_weather_get_radar": {}}
	p, err := mcp.Start(context.Background(), configured(t, weatherOnly), mcp.Options{
		Timers: &clock{}, Log: logs.logger(), Dial: houses{"weather": h}.dial, Native: native,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()
	if specs := p.Specs(); len(specs) != 1 || specs["mcp_weather_get_alerts"].Name == "" {
		t.Errorf("specs = %v, want get_alerts alone", specs)
	}
	for _, want := range []string{"their names are the same once made a tool name", "chorusd already has a tool by its name"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log does not say %q:\n%s", want, logs)
		}
	}
}

// The household trusts its weather server: every tool runs for anyone, the
// forecast without a yes and with longer to answer, the account-deleting
// tool not at all, and the location history for identified people only.
//
// verifies SPEC §6
func TestTheHouseholdRelaxesPolicyDeliberately(t *testing.T) {
	h := weather()
	for _, name := range []string{"delete_account", "get_location_history", "get_alerts"} {
		h.tool(&sdk.Tool{Name: name, Description: "Weather account."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
			return text("ok"), nil
		})
	}
	logs := &logBuffer{}
	p := start(t, configured(t, weatherOnly+`
    policy:
      scope: household
    tools:
      get_forecast:
        requires_confirmation: false
        timeout_ms: 8000
      delete_account:
        offer: false
      get_location_history:
        scope: person
      get_pollen:
        requires_confirmation: false
`), houses{"weather": h}, &clock{}, logs)
	specs := p.Specs()
	if _, ok := specs["mcp_weather_delete_account"]; ok {
		t.Error("delete_account is offered against offer: false")
	}
	cases := []struct {
		name    string
		scope   registry.Scope
		unknown string
		confirm bool
		timeout time.Duration
	}{
		{"mcp_weather_get_forecast", registry.ScopeHousehold, "", false, 8 * time.Second},
		{"mcp_weather_get_alerts", registry.ScopeHousehold, "", true, 5 * time.Second},
		{"mcp_weather_get_location_history", registry.ScopePerson, "deny", true, 5 * time.Second},
	}
	for _, c := range cases {
		s, ok := specs[c.name]
		if !ok {
			t.Errorf("%s is not offered", c.name)
			continue
		}
		if s.Scope != c.scope || s.UnknownSpeaker != c.unknown || s.RequiresConfirmation != c.confirm || s.Timeout != c.timeout {
			t.Errorf("%s: scope %s, unknown %q, confirm %t, timeout %v; want %s, %q, %t, %v",
				c.name, s.Scope, s.UnknownSpeaker, s.RequiresConfirmation, s.Timeout, c.scope, c.unknown, c.confirm, c.timeout)
		}
	}
	if desc := specs["mcp_weather_get_forecast"].ModelDescription; strings.Contains(desc, "yes") {
		t.Errorf("an unconfirmed forecast still tells the model to ask: %q", desc)
	}
	if !strings.Contains(logs.String(), "names a tool the server does not list") || !strings.Contains(logs.String(), "get_pollen") {
		t.Errorf("a policy for a tool the server does not list went unremarked:\n%s", logs)
	}
}

// A tool whose schema the registry cannot hold is refused with a reason,
// and the rest of the server's tools are still offered.
//
// verifies SPEC §6
func TestAToolTheRegistryCannotHoldIsRefusedAlone(t *testing.T) {
	h := weather()
	h.tool(&sdk.Tool{
		Name:        "set_units",
		Description: "Set units.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"confirmation":{"type":"string"}}}`),
	}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) { return text("ok"), nil })
	logs := &logBuffer{}
	p := start(t, configured(t, weatherOnly), houses{"weather": h}, &clock{}, logs)
	if specs := p.Specs(); len(specs) != 1 || specs["mcp_weather_get_forecast"].Name == "" {
		t.Errorf("specs = %v, want the forecast alone", specs)
	}
	if !strings.Contains(logs.String(), "set_units") || !strings.Contains(logs.String(), "the name is the orchestrator's") {
		t.Errorf("the refusal went unexplained:\n%s", logs)
	}
}

// Two servers' tools are offered side by side, each under its own prefix.
//
// verifies SPEC §6
func TestEachServersToolsAreOfferedUnderItsName(t *testing.T) {
	notes := newHouse("notes")
	notes.tool(&sdk.Tool{Name: "add_item", Description: "Add to the shopping list."}, func(context.Context, map[string]any) (*sdk.CallToolResult, error) {
		return text("Added."), nil
	})
	p := start(t, configured(t, weatherOnly+`
  notes:
    command: [/usr/local/bin/notes-mcp, --stdio]
`), houses{"weather": weather(), "notes": notes}, &clock{}, &logBuffer{})
	var names []string
	for name := range p.Specs() {
		names = append(names, name)
	}
	if len(names) != 2 || p.Specs()["mcp_notes_add_item"].Name == "" || p.Specs()["mcp_weather_get_forecast"].Name == "" {
		t.Errorf("specs = %q, want mcp_notes_add_item and mcp_weather_get_forecast", names)
	}
}
