package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/ops"
	"github.com/teagan42/chorus/internal/otel"
	coltracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/collector/trace/v1"
	tracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/trace/v1"
	"github.com/teagan42/chorus/internal/session"
)

// otlpReceiver is an OTLP/HTTP collector on loopback: it keeps every span
// it is sent, decoded from the protobuf the wire carried.
type otlpReceiver struct {
	*httptest.Server
	mu    sync.Mutex
	spans []*tracepb.Span
	posts int
}

func newReceiver(t *testing.T) *otlpReceiver {
	t.Helper()
	r := &otlpReceiver{}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var export coltracepb.ExportTraceServiceRequest
		if req.URL.Path != "/v1/traces" || proto.Unmarshal(body, &export) != nil {
			http.Error(w, "not OTLP/HTTP", http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.posts++
		for _, rs := range export.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				r.spans = append(r.spans, ss.Spans...)
			}
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *otlpReceiver) received() []*tracepb.Span {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.spans)
}

func (r *otlpReceiver) postCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts
}

// outline draws received spans as their parents hold them, by name only:
// the rig's clock does not move, so order among siblings is by name.
func outline(spans []*tracepb.Span) string {
	ids := map[string]bool{}
	for _, s := range spans {
		ids[string(s.SpanId)] = true
	}
	kids := map[string][]*tracepb.Span{}
	for _, s := range spans {
		parent := string(s.ParentSpanId)
		if !ids[parent] {
			parent = ""
		}
		kids[parent] = append(kids[parent], s)
	}
	var b strings.Builder
	var draw func(parent string, depth int)
	draw = func(parent string, depth int) {
		list := kids[parent]
		slices.SortStableFunc(list, func(x, y *tracepb.Span) int { return strings.Compare(label(x), label(y)) })
		for _, s := range list {
			b.WriteString(strings.Repeat("  ", depth) + label(s) + "\n")
			draw(string(s.SpanId), depth+1)
		}
	}
	draw("", 0)
	return b.String()
}

func label(s *tracepb.Span) string {
	l := s.Name
	for _, a := range s.Attributes {
		if a.Key == "chorus.satellite" {
			l += " " + a.Value.GetStringValue()
		}
	}
	if s.Status.GetCode() == tracepb.Status_STATUS_CODE_ERROR {
		l += " ERROR(" + s.Status.GetMessage() + ")"
	}
	return l
}

// Alan turns the kitchen lights off from the kitchen, walks to the office
// and asks for the hallway's too, then the house stops. The daemon's own
// journal reaches a real OTLP/HTTP collector as one trace: the kitchen's
// session closed by the migration, the office's resumed one closed by the
// shutdown, each turn's ask with the Home Assistant call it made and the
// follow-up ask that spoke. What was sent is what reading the daemon's log
// afterwards derives, and none of Alan's words left the house (ADR-0101).
//
// verifies SPEC §8, §4.5, §13
func TestTheDaemonExportsAConversationThatFollowsAlanAsOneTrace(t *testing.T) {
	const (
		kitchenOff = "The kitchen lights are off."
		hallwayOff = "The hallway light is off too."
	)
	collector := newReceiver(t)
	metrics := ops.NewMetrics("0.31.0")
	exporter := otel.New(otel.Config{
		Settings: otel.Settings{
			Endpoint: collector.URL + "/v1/traces",
			Resource: []otel.Attr{otel.String("service.name", "chorusd")},
		},
		Version: "0.31.0", Timers: neverTimers{}, Meter: metrics,
	})

	var lights sync.Mutex
	var switched []string
	hass := session.ToolFunc(func(_ context.Context, args string) (string, error) {
		lights.Lock()
		switched = append(switched, args)
		lights.Unlock()
		return `[]`, nil
	})
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	var asking sync.Mutex
	asks := 0
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		asking.Lock()
		defer asking.Unlock()
		asks++
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && asks == 1:
			return []session.Action{session.ToolCall{
				ID: "call_k1", Tool: "ha_call_service",
				Args: `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`,
			}, done}
		case last.Kind == journal.EntryHeard:
			return []session.Action{session.ToolCall{
				ID: "call_h1", Tool: "ha_call_service",
				Args: `{"domain":"light","service":"turn_off","entity_id":"light.hallway"}`,
			}, done}
		case asks == 2:
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: kitchenOff, Last: true}, done}
		}
		return []session.Action{session.SpeechDelta{CallID: "call_s2", Text: hallwayOff, Last: true}, done}
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.tools = map[string]session.Tool{"ha_call_service": hass}
		d.Ops = &opsDeps{Metrics: metrics}
		d.Traces = exporter
	})
	kitchen := r.join(t, kitchenIP)
	office := r.join(t, officeIP)

	kitchen.SendWake(t, "hey_eddie")
	r.utter(t, kitchen, r.line("turn off the kitchen lights", alan))
	kitchen.AwaitTTS(t, 2*len(kitchenOff))
	kitchen.PlayAll(t)
	r.spoken(t, "kitchen", 1)

	office.SendWake(t, "hey_eddie")
	r.utter(t, office, r.line("and the hallway light", alan))
	// The kitchen's session closed as the conversation moved: sent at once.
	await(t, "the kitchen's session at the collector", func() bool { return collector.postCount() >= 1 })
	office.AwaitTTS(t, 2*len(hallwayOff))
	office.PlayAll(t)
	r.spoken(t, "office", 2)

	r.cancel()
	if err := r.exit(t); err != nil {
		t.Fatalf("run returned %v", err)
	}

	got := collector.received()
	diff := func(what, got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("%s:\n%s\nwant:\n%s", what, got, want)
		}
	}
	diff("the exported trace", outline(got), `chorus.session kitchen
  chorus.turn
    chat qwen3-32b
      execute_tool ha_call_service
    chat qwen3-32b
      execute_tool speak
chorus.session office ERROR(device_lost)
  chorus.turn
    chat qwen3-32b
      execute_tool ha_call_service
    chat qwen3-32b
      execute_tool speak
`)
	for _, s := range got {
		if string(s.TraceId) != string(got[0].TraceId) {
			t.Errorf("%s is in another trace: the conversation did not follow Alan", s.Name)
		}
		for _, a := range s.Attributes {
			v := a.Value.GetStringValue()
			for _, said := range []string{"turn off the kitchen lights", "and the hallway light", kitchenOff, hallwayOff, "light.kitchen"} {
				if strings.Contains(v, said) {
					t.Errorf("%s carries %s = %q with words off", s.Name, a.Key, v)
				}
			}
		}
	}

	// What reached the collector is what the log the daemon wrote derives.
	var conv []journal.Event
	for _, e := range r.store.events() {
		if e.ConversationID == r.store.ofKind(journal.KindSessionOpened)[0].ConversationID {
			conv = append(conv, e)
		}
	}
	want := map[string]string{}
	for _, s := range otel.Spans(conv, otel.Options{}) {
		want[string(s.ID[:])] = s.Name
	}
	if len(got) != len(want) {
		t.Errorf("collector got %d spans, the daemon's log derives %d", len(got), len(want))
	}
	for _, s := range got {
		if want[string(s.SpanId)] != s.Name {
			t.Errorf("collector got %s %x, which the daemon's log does not derive", s.Name, s.SpanId)
		}
	}

	if exported := scrape(t, r, "chorus_otel_spans_exported_total"); exported != float64(len(want)) {
		t.Errorf("/metrics counted %v spans exported, want %d", exported, len(want))
	}
	lights.Lock()
	defer lights.Unlock()
	if len(switched) != 2 {
		t.Errorf("Home Assistant switched %v, want the kitchen and the hallway", switched)
	}
}

// scrape reads one sample from the daemon's /metrics.
func scrape(t *testing.T, r *rig, series string) float64 {
	t.Helper()
	rec := httptest.NewRecorder()
	r.dm.ops.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if name, value, ok := strings.Cut(line, " "); ok && name == series {
			v, err := strconv.ParseFloat(value, 64)
			if err != nil {
				t.Fatalf("%s = %q: %v", series, value, err)
			}
			return v
		}
	}
	return -1
}

// The export is configured as every OpenTelemetry SDK is, off until an
// endpoint is named, and a variable set but unusable stops startup naming
// it, beside the rest of what is missing (SPEC §13).
//
// verifies SPEC §13
func TestTheTraceExportIsConfiguredByTheStandardVariables(t *testing.T) {
	if cfg := configFromEnv(lookup(complete())); cfg.Traces.Enabled() || cfg.validate() != nil {
		t.Errorf("with no endpoint: enabled %v, validate %v; want off and valid", cfg.Traces.Enabled(), cfg.validate())
	}

	env := complete()
	env["OTEL_EXPORTER_OTLP_ENDPOINT"] = "http://otel-collector:4318"
	env["OTEL_EXPORTER_OTLP_HEADERS"] = "Authorization=Basic%20cGstbGYtdGVhZ2FuOnNrLWxmLWhvdXNl"
	cfg := configFromEnv(lookup(env))
	if err := cfg.validate(); err != nil || cfg.Traces.Endpoint != "http://otel-collector:4318/v1/traces" || cfg.Traces.Words {
		t.Errorf("collector set: %+v, validate %v", cfg.Traces, err)
	}

	env["OTEL_EXPORTER_OTLP_PROTOCOL"] = "grpc"
	if err := configFromEnv(lookup(env)).validate(); err == nil || !strings.Contains(err.Error(), "OTEL_EXPORTER_OTLP_PROTOCOL") {
		t.Errorf("gRPC validated: %v", err)
	}
}
