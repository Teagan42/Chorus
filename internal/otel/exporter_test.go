package otel_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/otel"
	coltracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/collector/trace/v1"
	commonpb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/common/v1"
	tracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/trace/v1"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

const patience = 5 * time.Second

func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// timers fires only when the test says, so no export waits on the wall.
type timers struct {
	mu    sync.Mutex
	waits map[time.Duration][]chan time.Time
}

func newTimers() *timers { return &timers{waits: map[time.Duration][]chan time.Time{}} }

func (v *timers) After(d time.Duration) <-chan time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	ch := make(chan time.Time, 1)
	v.waits[d] = append(v.waits[d], ch)
	return ch
}

func (v *timers) pending(d time.Duration) int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return len(v.waits[d])
}

// fire ends every wait of d once one is pending.
func (v *timers) fire(t *testing.T, d time.Duration) {
	t.Helper()
	await(t, d.String()+" timer", func() bool { return v.pending(d) > 0 })
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, ch := range v.waits[d] {
		ch <- time.Time{}
	}
	delete(v.waits, d)
}

// collector is an OTLP/HTTP receiver: it decodes what it is sent and
// answers with the next status it was told to, then 200.
type collector struct {
	*httptest.Server
	mu       sync.Mutex
	got      []*coltracepb.ExportTraceServiceRequest
	headers  []http.Header
	answers  []int
	rejected int64
	hang     bool
	// held, when set, holds each request until it is closed.
	held chan struct{}
	took chan struct{}
}

func newCollector(t *testing.T) *collector {
	c := &collector{}
	c.Server = httptest.NewServer(http.HandlerFunc(c.serve))
	t.Cleanup(c.Close)
	return c
}

func (c *collector) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	hang, held := c.hang, c.held
	c.mu.Unlock()
	if hang {
		// Read first: the server notices the client hang up only after.
		_, _ = io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
		return
	}
	if held != nil {
		c.took <- struct{}{}
		<-held
	}
	body, _ := io.ReadAll(r.Body)
	var req coltracepb.ExportTraceServiceRequest
	if r.URL.Path != "/v1/traces" || r.Header.Get("Content-Type") != "application/x-protobuf" || proto.Unmarshal(body, &req) != nil {
		http.Error(w, "not OTLP/HTTP protobuf", http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	status := http.StatusOK
	if len(c.answers) > 0 {
		status, c.answers = c.answers[0], c.answers[1:]
	}
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	c.got = append(c.got, &req)
	c.headers = append(c.headers, r.Header.Clone())
	answer := &coltracepb.ExportTraceServiceResponse{}
	if c.rejected > 0 {
		answer.PartialSuccess = &coltracepb.ExportTracePartialSuccess{RejectedSpans: c.rejected, ErrorMessage: "span name too long for the index"}
	}
	out, _ := proto.Marshal(answer)
	w.Header().Set("Content-Type", "application/x-protobuf")
	_, _ = w.Write(out)
}

func (c *collector) spans() []*tracepb.Span {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*tracepb.Span
	for _, r := range c.got {
		for _, rs := range r.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				out = append(out, ss.Spans...)
			}
		}
	}
	return out
}

func (c *collector) requests() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// meter keeps what the exporter counted.
type meter struct {
	mu       sync.Mutex
	exported int
	events   int
	dropped  map[string]int
}

func (m *meter) EventsDropped(n int) { m.mu.Lock(); m.events += n; m.mu.Unlock() }
func (m *meter) SpansExported(n int) { m.mu.Lock(); m.exported += n; m.mu.Unlock() }
func (m *meter) SpansDropped(reason string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dropped == nil {
		m.dropped = map[string]int{}
	}
	m.dropped[reason] += n
}

func (m *meter) read() (exported, events int, dropped map[string]int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d := map[string]int{}
	for k, v := range m.dropped {
		d[k] = v
	}
	return m.exported, m.events, d
}

type logs struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logs) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }

func (l *logs) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// exporting is an exporter running against c on virtual timers.
type exporting struct {
	x      *otel.Exporter
	timers *timers
	meter  *meter
	logs   *logs
	cancel context.CancelFunc
	done   chan struct{}
}

func export(t *testing.T, c *collector, tweak ...func(*otel.Config)) *exporting {
	t.Helper()
	e := &exporting{timers: newTimers(), meter: &meter{}, logs: &logs{}, done: make(chan struct{})}
	cfg := otel.Config{
		Settings: otel.Settings{
			Endpoint: c.URL + "/v1/traces",
			Headers:  map[string]string{"Authorization": "Basic cGstbGYtdGVhZ2FuOnNrLWxmLWhvdXNl"},
			Timeout:  otel.DefaultTimeout,
			Resource: []otel.Attr{otel.String("service.name", "chorusd"), otel.String("deployment.environment", "home")},
		},
		Version: "0.31.0", Timers: e.timers, Meter: e.meter,
		Log: slog.New(slog.NewTextHandler(e.logs, nil)),
	}
	for _, f := range tweak {
		f(&cfg)
	}
	e.x = otel.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	go func() {
		e.x.Run(ctx)
		close(e.done)
	}()
	t.Cleanup(e.stop)
	return e
}

func (e *exporting) stop() {
	e.cancel()
	<-e.done
}

func (e *exporting) feed(events []journal.Event) {
	for _, ev := range events {
		e.x.Appended(ev)
	}
}

func str(kv []*commonpb.KeyValue, key string) string {
	for _, a := range kv {
		if a.Key == key {
			return a.Value.GetStringValue()
		}
	}
	return ""
}

// Teagan's weather and the garage reach a real OTLP/HTTP receiver as each
// session closes: protobuf on /v1/traces, with the credential, under
// chorusd's resource and version, each span as the journal derived it.
//
// verifies SPEC §8
func TestEachSessionsSpansReachTheCollectorAsItCloses(t *testing.T) {
	c := newCollector(t)
	e := export(t, c)
	day := thursday(t)

	e.feed(day[household.ConvWeather])
	await(t, "the weather's spans", func() bool { return c.requests() == 1 })
	e.feed(day[household.ConvGarage])
	await(t, "the garage's spans", func() bool { return c.requests() == 2 })

	c.mu.Lock()
	first, auth := c.got[0], c.headers[0].Get("Authorization")
	c.mu.Unlock()
	if auth != "Basic cGstbGYtdGVhZ2FuOnNrLWxmLWhvdXNl" {
		t.Errorf("Authorization %q, want the configured credential", auth)
	}
	rs := first.ResourceSpans[0]
	if str(rs.Resource.Attributes, "service.name") != "chorusd" || str(rs.Resource.Attributes, "service.version") != "0.31.0" ||
		str(rs.Resource.Attributes, "deployment.environment") != "home" {
		t.Errorf("resource %v", rs.Resource.Attributes)
	}
	if s := rs.ScopeSpans[0].Scope; s.Name != "github.com/teagan42/chorus/internal/otel" || s.Version != "0.31.0" {
		t.Errorf("scope %v", s)
	}

	want := map[string]otel.Span{}
	for _, log := range [][]journal.Event{day[household.ConvWeather], day[household.ConvGarage]} {
		for _, s := range otel.Spans(log, otel.Options{}) {
			want[s.ID.String()] = s
		}
	}
	got := c.spans()
	if len(got) != len(want) {
		t.Fatalf("collector got %d spans, the logs derive %d", len(got), len(want))
	}
	for _, p := range got {
		var id otel.SpanID
		copy(id[:], p.SpanId)
		w, ok := want[id.String()]
		if !ok {
			t.Errorf("collector got span %s %x the logs do not derive", p.Name, p.SpanId)
			continue
		}
		var trace otel.TraceID
		copy(trace[:], p.TraceId)
		var parent otel.SpanID
		copy(parent[:], p.ParentSpanId)
		if p.Name != w.Name || trace != w.Trace || parent != w.Parent ||
			p.StartTimeUnixNano != uint64(w.Start.UnixNano()) || p.EndTimeUnixNano != uint64(w.End.UnixNano()) ||
			len(p.Events) != len(w.Events) || len(p.Attributes) != len(w.Attrs) {
			t.Errorf("collector got %v\nwant %+v", p, w)
		}
		if w.Parent.IsZero() && p.ParentSpanId != nil {
			t.Errorf("root %s sent with a parent id", p.Name)
		}
	}

	byName := map[string]*tracepb.Span{}
	for _, p := range got {
		byName[p.Name+"/"+str(p.Attributes, "gen_ai.tool.name")+"/"+str(p.Attributes, "chorus.tool.outcome")] = p
	}
	timedOut := byName["execute_tool ha_get_state/ha_get_state/timed_out"]
	if timedOut == nil || timedOut.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR || timedOut.Status.GetMessage() != "timed_out" {
		t.Errorf("the garage sensor's call = %v, want an ERROR naming the timeout", timedOut)
	}
	ask := byName["chat qwen3-32b@1//"]
	if ask == nil || ask.Kind != tracepb.Span_SPAN_KIND_CLIENT {
		t.Fatalf("no client span for the model's ask: %v", ask)
	}
	for _, a := range ask.Attributes {
		if a.Key == "gen_ai.response.finish_reasons" {
			if v := a.Value.GetArrayValue().GetValues(); len(v) != 1 || v[0].GetStringValue() != "stop" {
				t.Errorf("finish reasons %v", v)
			}
		}
		if a.Key == "chorus.seq" && a.Value.GetIntValue() == 0 {
			t.Error("chorus.seq sent as zero")
		}
	}
	if exported, _, _ := e.meter.read(); exported != len(want) {
		t.Errorf("meter counted %d exported, want %d", exported, len(want))
	}
}

// The collector restarts mid-evening: the spans are held, logged once, and
// sent at the next tick once it answers again (OTLP's retryable answers).
//
// verifies SPEC §8
func TestSpansWaitOutACollectorThatIsDown(t *testing.T) {
	c := newCollector(t)
	c.answers = []int{http.StatusServiceUnavailable}
	e := export(t, c)
	e.feed(thursday(t)[household.ConvList])

	await(t, "the refusal logged", func() bool { return strings.Contains(e.logs.String(), "not reaching the collector") })
	if c.requests() != 0 {
		t.Fatal("the collector took spans it refused")
	}
	e.timers.fire(t, otel.DefaultInterval)
	await(t, "the retry delivered", func() bool { return c.requests() == 1 })
	await(t, "the recovery logged", func() bool { return strings.Contains(e.logs.String(), "reaching the collector again") })
	if got := len(c.spans()); got != 5 {
		t.Errorf("retry delivered %d spans, want the list conversation's 5", got)
	}
	if strings.Count(e.logs.String(), "not reaching the collector") != 1 {
		t.Errorf("the outage was logged more than once:\n%s", e.logs.String())
	}
}

// A collector that refuses the batch outright is not asked again: the spans
// are dropped and counted. One that takes a batch but rejects some says so
// in its answer, and those are counted too.
//
// verifies SPEC §8
func TestRefusedSpansAreCountedNotRetried(t *testing.T) {
	c := newCollector(t)
	c.answers = []int{http.StatusBadRequest}
	e := export(t, c)
	day := thursday(t)

	e.feed(day[household.ConvList])
	await(t, "the refusal counted", func() bool { _, _, d := e.meter.read(); return d[otel.DroppedRejected] == 5 })
	e.timers.fire(t, otel.DefaultInterval)

	c.mu.Lock()
	c.rejected = 2
	c.mu.Unlock()
	e.feed(day[household.ConvLock])
	await(t, "the lock's spans", func() bool { return c.requests() == 1 })
	await(t, "the partial rejection counted", func() bool { _, _, d := e.meter.read(); return d[otel.DroppedRejected] == 7 })
	// The summary, written after the close, waits for the next tick.
	if got := len(c.spans()); got != 5 {
		t.Errorf("collector got %d spans, want only the lock session's 5: the refused batch came back", got)
	}
}

// A collector that never answers is given up on at the timeout, on the
// injected timers, and its spans are held to retry.
//
// verifies SPEC §8
func TestAHungCollectorIsGivenUpOnAtTheTimeout(t *testing.T) {
	c := newCollector(t)
	c.hang = true
	e := export(t, c, func(cfg *otel.Config) { cfg.Timeout = 2500 * time.Millisecond })
	e.feed(thursday(t)[household.ConvList])

	e.timers.fire(t, 2500*time.Millisecond)
	await(t, "the timeout logged", func() bool { return strings.Contains(e.logs.String(), "not reaching the collector") })
	c.mu.Lock()
	c.hang = false
	c.mu.Unlock()
	e.timers.fire(t, otel.DefaultInterval)
	await(t, "the retry delivered", func() bool { return c.requests() == 1 })
}

// Stopping sends what has ended but not yet been sent. What is still open
// dies with the process, as it would on a crash.
//
// verifies SPEC §8
func TestStoppingSendsWhatHasEnded(t *testing.T) {
	c := newCollector(t)
	e := export(t, c)
	log := thursday(t)[household.ConvList]
	// Up to the completion: the turn's calls and ask have ended, the session not.
	e.feed(log[:len(log)-1])
	await(t, "the events queued", func() bool { return e.timers.pending(otel.DefaultInterval) == 1 })
	e.stop()

	if got := c.spans(); len(got) != 3 {
		var names []string
		for _, s := range got {
			names = append(names, s.Name)
		}
		t.Errorf("on stopping sent %v, want the two calls and the ask", names)
	}
}

// The daemon stopping while a batch is on its way does not cut it off: the
// collector takes it, and it is not held and sent again on the way out.
//
// verifies SPEC §8
func TestStoppingMidSendSendsOnce(t *testing.T) {
	c := newCollector(t)
	c.held, c.took = make(chan struct{}), make(chan struct{}, 1)
	e := export(t, c)
	e.feed(thursday(t)[household.ConvList])

	<-c.took
	e.cancel()
	close(c.held)
	<-e.done

	if got := len(c.spans()); got != 5 || c.requests() != 1 {
		t.Errorf("collector got %d spans in %d requests, want the list conversation's 5 once", got, c.requests())
	}
	if strings.Contains(e.logs.String(), "not reaching") {
		t.Errorf("a send cut off by stopping:\n%s", e.logs.String())
	}
}

// The writer is never held up: with the queue full an event is dropped and
// counted, and the append returns at once.
//
// verifies SPEC §8
func TestAFullQueueDropsRatherThanBlocking(t *testing.T) {
	m := &meter{}
	x := otel.New(otel.Config{Settings: otel.Settings{Endpoint: "http://otel-collector:4318/v1/traces"}, Timers: newTimers(), Meter: m})
	ev := thursday(t)[household.ConvList][0]
	for range 4097 {
		x.Appended(ev)
	}
	if _, events, _ := m.read(); events != 1 {
		t.Errorf("dropped %d events, want the one past the queue", events)
	}
}
