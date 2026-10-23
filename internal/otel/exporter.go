package otel

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/journal"
	coltracepb "github.com/teagan42/chorus/internal/pb/opentelemetry/proto/collector/trace/v1"
)

// Bounds on what the exporter holds. Appends are never held up: an event
// that finds the queue full is dropped and counted.
const (
	queueEvents = 4096
	maxBatch    = 512
	maxBacklog  = 4096
)

// DefaultInterval is how often spans that ended are sent, when nothing
// sends them sooner: a session's close sends at once.
const DefaultInterval = 5 * time.Second

// shutdownTimeout bounds the last send, as the ops listener's close is
// bounded: a stopping daemon does not wait out a hung collector.
const shutdownTimeout = 5 * time.Second

// Timers is the injected scheduler: wall timers in the daemon, a virtual
// clock in tests.
type Timers interface {
	After(time.Duration) <-chan time.Time
}

// Meter counts what the exporter did, for chorusd's /metrics.
type Meter interface {
	EventsDropped(n int)
	SpansExported(n int)
	SpansDropped(reason string, n int)
}

// The reasons a span is dropped.
const (
	DroppedRejected = "rejected" // the collector refused it, and retrying would not help
	DroppedBacklog  = "backlog"  // the collector was down long enough to fill the backlog
)

// Config is an exporter's settings and what it runs on.
type Config struct {
	Settings
	// Version is chorusd's, as the scope and service.version name it.
	Version  string
	Client   *http.Client
	Timers   Timers
	Interval time.Duration
	Meter    Meter
	Log      *slog.Logger
}

// Exporter is the journal's Observer that sends each span the log ends to an
// OTLP/HTTP collector. Appended only queues; a goroutine builds and sends.
type Exporter struct {
	cfg      Config
	resource []Attr
	in       chan journal.Event
	failing  bool
}

var _ journal.Observer = (*Exporter)(nil)

// New returns an exporter; Run sends.
func New(cfg Config) *Exporter {
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Meter == nil {
		cfg.Meter = nopMeter{}
	}
	res := append([]Attr(nil), cfg.Resource...)
	if cfg.Version != "" {
		res = append(res, String("service.version", cfg.Version))
	}
	return &Exporter{cfg: cfg, resource: res, in: make(chan journal.Event, queueEvents)}
}

// Appended queues the event. It never blocks the writer (ADR-0068).
func (x *Exporter) Appended(e journal.Event) {
	select {
	case x.in <- e:
	default:
		x.cfg.Meter.EventsDropped(1)
	}
}

// AppendFailed is nothing to a trace: what never reached the log never
// happened as far as a reader of it can tell.
func (x *Exporter) AppendFailed(journal.Kind, error) {}

// Run builds spans from the queue and sends them until ctx ends, then sends
// what it holds once more, bounded by the timeout.
func (x *Exporter) Run(ctx context.Context) {
	b := NewBuilder(Options{Words: x.cfg.Words})
	// A send is never cancelled partway: one the collector took but this
	// side gave up on would be held and sent twice.
	sends := context.WithoutCancel(ctx)
	var pending []Span
	tick := x.cfg.Timers.After(x.cfg.Interval)
	for {
		select {
		case e := <-x.in:
			pending = append(pending, b.Add(e)...)
			if e.Kind == journal.KindSessionClosed || len(pending) >= maxBatch {
				pending = x.flush(sends, pending, x.cfg.Timeout)
			}
		case <-tick:
			pending = x.flush(sends, pending, x.cfg.Timeout)
			tick = x.cfg.Timers.After(x.cfg.Interval)
		case <-ctx.Done():
		drain:
			for {
				select {
				case e := <-x.in:
					pending = append(pending, b.Add(e)...)
				default:
					break drain
				}
			}
			// Open spans die with the process, as they would on a crash.
			x.flush(sends, pending, min(x.cfg.Timeout, shutdownTimeout))
			return
		}
	}
}

// flush sends what is pending in batches and returns what is left to retry.
func (x *Exporter) flush(ctx context.Context, pending []Span, timeout time.Duration) []Span {
	for len(pending) > 0 {
		n := min(len(pending), maxBatch)
		err := x.send(ctx, pending[:n], timeout)
		var refused refusal
		switch {
		case err == nil:
			x.cfg.Meter.SpansExported(n)
		case errors.As(err, &refused) && !refused.retry:
			x.cfg.Meter.SpansDropped(DroppedRejected, n)
		default:
			x.report(err)
			if over := len(pending) - maxBacklog; over > 0 {
				x.cfg.Meter.SpansDropped(DroppedBacklog, over)
				pending = pending[over:]
			}
			return pending
		}
		x.report(err)
		pending = pending[n:]
	}
	return nil
}

// report logs the collector going away, and coming back, once each.
func (x *Exporter) report(err error) {
	var refused refusal
	switch {
	case err == nil && x.failing:
		x.failing = false
		x.cfg.Log.Info("otel: spans are reaching the collector again", "endpoint", x.cfg.Endpoint)
	case errors.As(err, &refused) && !refused.retry:
		x.cfg.Log.Warn("otel: the collector refused spans; dropped", "endpoint", x.cfg.Endpoint, "err", err)
	case err != nil && !x.failing:
		x.failing = true
		x.cfg.Log.Warn("otel: spans are not reaching the collector; holding them to retry", "endpoint", x.cfg.Endpoint, "err", err)
	}
}

// refusal is an answer from the collector other than success. OTLP says
// which are worth sending again.
type refusal struct {
	status int
	body   string
	retry  bool
}

func (r refusal) Error() string { return fmt.Sprintf("answered %d: %s", r.status, r.body) }

// send posts one batch, bounded by timeout on the injected timers.
func (x *Exporter) send(ctx context.Context, spans []Span, timeout time.Duration) error {
	body, err := proto.Marshal(Request(x.resource, x.cfg.Version, spans))
	if err != nil {
		return refusal{body: err.Error()}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-x.cfg.Timers.After(timeout):
			cancel()
		case <-done:
		}
	}()

	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, x.cfg.Endpoint, bytes.NewReader(body))
	if err != nil {
		return refusal{body: err.Error()}
	}
	hr.Header.Set("Content-Type", "application/x-protobuf")
	for k, v := range x.cfg.Headers {
		hr.Header.Set(k, v)
	}
	resp, err := x.cfg.Client.Do(hr)
	if err != nil {
		return fmt.Errorf("POST %s: %w", x.cfg.Endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return refusal{status: resp.StatusCode, body: string(answer), retry: true}
	default:
		return refusal{status: resp.StatusCode, body: string(answer)}
	}
	x.partial(answer)
	return nil
}

// partial counts the spans a collector took the batch but refused.
func (x *Exporter) partial(answer []byte) {
	var r coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(answer, &r); err != nil || r.GetPartialSuccess().GetRejectedSpans() == 0 {
		return
	}
	ps := r.GetPartialSuccess()
	x.cfg.Meter.SpansDropped(DroppedRejected, int(ps.GetRejectedSpans()))
	x.cfg.Log.Warn("otel: the collector refused some spans", "endpoint", x.cfg.Endpoint,
		"rejected", ps.GetRejectedSpans(), "message", ps.GetErrorMessage())
}

type nopMeter struct{}

func (nopMeter) EventsDropped(int)        {}
func (nopMeter) SpansExported(int)        {}
func (nopMeter) SpansDropped(string, int) {}
