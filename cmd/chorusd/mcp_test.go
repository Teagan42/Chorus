package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/mcp"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

const (
	forecastTool = "mcp_weather_get_forecast"
	askWeather   = "what's the weather in lisbon tomorrow"
	lisbonArgs   = `{"city":"Lisbon","days":1}`
	checkFirst   = "Shall I ask the weather server about Lisbon?"
	sunny        = "Sunny and 21°C in Lisbon."
)

// weatherServer is the household's MCP weather server, in process: each
// dial is a fresh session over a pipe. hold, when set, keeps every forecast
// until its context ends, counting the cancellations it saw.
type weatherServer struct {
	srv       *sdk.Server
	hold      bool
	cancelled atomic.Int32

	mu    sync.Mutex
	calls []string
}

func newWeatherServer(hold bool) *weatherServer {
	w := &weatherServer{srv: sdk.NewServer(&sdk.Implementation{Name: "weather", Version: "1.0.0"}, nil), hold: hold}
	w.srv.AddTool(&sdk.Tool{
		Name:        "get_forecast",
		Description: "Get the weather forecast for a city.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string","description":"City name."},"days":{"type":"integer","description":"Days ahead."}},"required":["city"]}`),
	}, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		w.mu.Lock()
		w.calls = append(w.calls, string(req.Params.Arguments))
		w.mu.Unlock()
		if w.hold {
			<-ctx.Done()
			w.cancelled.Add(1)
			return nil, ctx.Err()
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: sunny}}}, nil
	})
	return w
}

func (w *weatherServer) dial(mcp.Server) (sdk.Transport, error) {
	client, server := net.Pipe()
	if _, err := w.srv.Connect(context.Background(), &sdk.IOTransport{Reader: server, Writer: server}, nil); err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	return &sdk.IOTransport{Reader: client, Writer: client}, nil
}

func (w *weatherServer) called() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.calls...)
}

// withWeather connects the weather server under policy (YAML under the
// server, empty for the defaults) and wires its tools into the daemon as
// buildProviders would.
func withWeather(t *testing.T, w *weatherServer, policy string) func(*deps) {
	t.Helper()
	cfg, err := mcp.Parse([]byte("servers:\n  weather:\n    url: http://weather.lan:8080/mcp\n"+policy), lookup(nil))
	if err != nil {
		t.Fatalf("mcp config: %v", err)
	}
	m, err := mcp.Start(context.Background(), cfg, mcp.Options{Timers: neverTimers{}, Dial: w.dial})
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	t.Cleanup(m.Close)
	specs, _, err := offerable(m)
	if err != nil {
		t.Fatalf("offerable: %v", err)
	}
	return func(d *deps) {
		d.tools, d.specs = m.Tools(), specs
	}
}

// staticTools is a provider whose tools are given.
type staticTools map[string]registry.ToolSpec

func (s staticTools) Specs() map[string]registry.ToolSpec { return s }
func (s staticTools) Tools() map[string]session.Tool {
	out := map[string]session.Tool{}
	for name := range s {
		out[name] = session.ToolFunc(func(context.Context, string) (string, error) { return "{}", nil })
	}
	return out
}

// forecastSpec is the weather server's forecast as the provider offers it.
func forecastSpec(t *testing.T) registry.ToolSpec {
	t.Helper()
	spec, err := registry.Provide(registry.ToolSpec{
		Name: forecastTool, Description: "Get the weather forecast for a city.",
		Params:      []registry.ParamSpec{{Name: "city", Type: "string", Description: "City name.", Required: true}},
		OnInterrupt: registry.InterruptCancel, Scope: registry.ScopePerson, UnknownSpeaker: "deny",
		Timeout: mcp.DefaultTimeout, RequiresConfirmation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// The MCP servers' tools are offered beside the registry's: the engine is
// sent them, so the tool-schema version every event records names them;
// the session enforces their policy; and their executors are wired.
//
// verifies SPEC §6, §8
func TestMCPToolsAreOfferedBesideTheRegistrys(t *testing.T) {
	cfg := configFromEnv(lookup(complete()))
	without, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler), nil)
	if err != nil {
		t.Fatalf("providers: %v", err)
	}
	extra := staticTools{forecastTool: forecastSpec(t)}
	with, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler), extra)
	if err != nil {
		t.Fatalf("providers with mcp: %v", err)
	}
	offered := registry.Offered()
	offered[forecastTool] = extra[forecastTool]
	if with.versions.ToolSchema != ollama.ToolSchemaVersion(offered) || with.versions.ToolSchema == without.versions.ToolSchema {
		t.Errorf("tool schema = %s (without %s), want the version of the registry's tools and the forecast", with.versions.ToolSchema, without.versions.ToolSchema)
	}
	if without.specs != nil {
		t.Errorf("specs without mcp = %d tools, want nil: the registry's own", len(without.specs))
	}
	if with.specs[forecastTool].Name == "" || with.specs["media_search"].Name == "" {
		t.Error("the session would not enforce both the forecast's and every declared tool's policy")
	}
	if _, ok := with.tools[forecastTool]; !ok {
		t.Errorf("tools = %v, want the forecast's executor beside Home Assistant's", with.tools)
	}
	if _, ok := with.tools["ha_call_service"]; !ok {
		t.Error("the forecast replaced Home Assistant's executors")
	}

	clash := staticTools{"ha_call_service": forecastSpec(t)}
	if _, err := buildProviders(cfg, nil, slog.New(slog.DiscardHandler), clash); err == nil {
		t.Error("a provided tool took Home Assistant's name")
	}
}

// CHORUS_MCP_CONFIG is read with the rest of the environment. Unset, no
// server is started; set to a file that will not load, startup says which.
//
// verifies SPEC §13
func TestTheMCPConfigurationIsOptIn(t *testing.T) {
	env := complete()
	if m, err := startMCP(context.Background(), configFromEnv(lookup(env)), lookup(env), neverTimers{}, nil); m != nil || err != nil {
		t.Errorf("unset: provider %v, error %v; want neither", m, err)
	}
	path := filepath.Join(t.TempDir(), "mcp.yaml")
	if err := os.WriteFile(path, []byte("servers:\n  home_weather:\n    url: http://weather.lan/mcp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env[mcp.ConfigEnv] = path
	cfg := configFromEnv(lookup(env))
	if cfg.MCPConfig != path {
		t.Fatalf("MCPConfig = %q, want %q", cfg.MCPConfig, path)
	}
	if _, err := startMCP(context.Background(), cfg, lookup(env), neverTimers{}, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), mcp.ConfigEnv) {
		t.Errorf("error %v, want it to name %s", err, mcp.ConfigEnv)
	}
}

// forecastModel asks the weather server when asked, asks the person when
// the call is held, calls again with the nonce on a yes, and says what the
// server answered.
func forecastModel() *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	n := 0
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == askWeather:
			return []session.Action{session.ToolCall{ID: "call_w1", Tool: forecastTool, Args: lisbonArgs}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: checkFirst, Last: true}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes":
			var held struct {
				Nonce string `json:"nonce"`
			}
			for _, e := range in.Dialogue {
				if e.Outcome == "confirmation_required" {
					_ = json.Unmarshal([]byte(e.Result), &held)
				}
			}
			n++
			args := strings.TrimSuffix(lisbonArgs, "}") + `,"confirmation":"` + held.Nonce + `"}`
			return []session.Action{session.ToolCall{ID: fmt.Sprintf("call_w%d", n+1), Tool: forecastTool, Args: args}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			var out struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal([]byte(last.Result), &out)
			return []session.Action{session.SpeechDelta{CallID: "call_s2", Text: out.Text, Last: true}, done}
		}
		return []session.Action{done}
	}}
}

// Alan asks the kitchen for Lisbon's weather tomorrow. The forecast comes
// from the household's MCP weather server, under the defaults: nothing
// reaches the server until Alan has heard the question and said yes; then
// it is called with exactly what the model wrote, less the nonce, and the
// kitchen says what it answered. The log holds the call by its namespaced
// name, which names the server.
//
// verifies SPEC §6, §8
func TestAlanAsksTheWeatherServerAndSaysYes(t *testing.T) {
	w := newWeatherServer(false)
	r := newRig(t, inventory(), withWeather(t, w, ""), func(d *deps) { d.engine = forecastModel() })
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line(askWeather, alan))
	dev.AwaitTTS(t, 2*len(checkFirst))
	dev.PlayAll(t)
	r.spoken(t, "kitchen", 1)
	if got := w.called(); len(got) != 0 {
		t.Fatalf("the weather server was called with %q before Alan said yes", got)
	}

	r.utter(t, dev, r.line("yes", alan))
	dev.AwaitTTS(t, 2*len(checkFirst)+2*len(sunny))
	dev.PlayAll(t)
	if got := r.spoken(t, "kitchen", 2).Fields["text"]; got != sunny {
		t.Errorf("the kitchen said %q, want %q", got, sunny)
	}
	if got := w.called(); len(got) != 1 || got[0] != lisbonArgs {
		t.Errorf("the weather server was called with %q, want %s once: no nonce", got, lisbonArgs)
	}
	var calls []string
	for _, e := range r.store.ofKind(journal.KindToolCalled) {
		if e.Fields["tool"] == forecastTool {
			calls = append(calls, e.Fields["call_id"])
		}
	}
	if len(calls) != 2 {
		t.Errorf("tool_called for %s = %q, want the held call and the confirmed one", forecastTool, calls)
	}
	var outcomes []string
	for _, e := range r.store.ofKind(journal.KindToolResult) {
		if e.Fields["call_id"] == "call_w1" || e.Fields["call_id"] == "call_w2" {
			outcomes = append(outcomes, e.Fields["outcome"]+" "+e.Fields["result_json"])
		}
	}
	if len(outcomes) != 2 || !strings.HasPrefix(outcomes[0], "confirmation_required ") || outcomes[1] != `ok {"text":"`+sunny+`"}` {
		t.Errorf("results = %q, want the hold and then the server's answer", outcomes)
	}
	if given := r.store.ofKind(journal.KindConfirmationGiven); len(given) != 1 || given[0].Fields["call_id"] != "call_w2" {
		t.Errorf("confirmation_given = %v, want call_w2 to redeem the nonce", given)
	}
}

// Alan wakes the kitchen, and the television asks it for the weather. Its
// voice matches nobody, and an MCP tool runs for identified people only by
// default, so the model is told so and the weather server is never called.
//
// verifies SPEC §5, §6
func TestAVoiceNobodyKnowsCannotRunAnMCPTool(t *testing.T) {
	w := newWeatherServer(false)
	r := newRig(t, inventory(), withWeather(t, w, ""), func(d *deps) { d.engine = forecastModel() })
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("hello", alan))
	r.utter(t, dev, r.line(askWeather, stranger))
	res := r.store.awaitKind(t, journal.KindToolResult, 1)
	if res.Fields["outcome"] != "error" || res.Fields["result_json"] != `{"error":"unidentified_speaker"}` {
		t.Errorf("result = %v, want unidentified_speaker", res.Fields)
	}
	if got := w.called(); len(got) != 0 {
		t.Errorf("the weather server was called with %q for a voice nobody knows", got)
	}
}

// The household lets anyone ask for the weather without a yes; Alan does. The server
// hangs; at the tool's five seconds the model is told timed_out, and the
// call is cancelled on the server too, not left running.
//
// verifies SPEC §4.4, §6, §7
func TestAnMCPCallPastItsTimeoutIsCancelledOnTheServer(t *testing.T) {
	w := newWeatherServer(true)
	clk := newClock()
	relaxed := "    policy:\n      scope: household\n      requires_confirmation: false\n"
	r := newRig(t, inventory(), withWeather(t, w, relaxed), func(d *deps) {
		d.engine, d.Clock, d.Timers = forecastModel(), clk, clk
	})
	dev := r.join(t, kitchenIP)

	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line(askWeather, alan))
	await(t, "the call to reach the weather server", func() bool { return len(w.called()) == 1 })
	clk.awaitWait(t, mcp.DefaultTimeout)
	clk.advance(mcp.DefaultTimeout)
	res := r.store.awaitKind(t, journal.KindToolResult, 1)
	if res.Fields["call_id"] != "call_w1" || res.Fields["outcome"] != "timed_out" {
		t.Errorf("result = %v, want call_w1 timed_out", res.Fields)
	}
	await(t, "the server to see the call cancelled", func() bool { return w.cancelled.Load() == 1 })
}
