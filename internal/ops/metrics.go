package ops

import (
	"context"
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/journal"
)

// firstAudioBuckets straddle SPEC §11's 700 ms target, out to a cold load.
var firstAudioBuckets = []float64{0.25, 0.5, 0.7, 1, 1.5, 2, 3, 5, 10}

// maxOpenCalls bounds the calls held for their result's tool label. A call
// whose result never comes is let go, oldest first.
const maxOpenCalls = 4096

// Metrics counts what the journal records as it is written, and what the
// blob store writes. It is the journal's Observer (ADR-0068).
type Metrics struct {
	reg *prometheus.Registry

	sessionsOpened prometheus.Counter
	sessionsClosed *prometheus.CounterVec
	bargeDetected  prometheus.Counter
	bargeRejected  *prometheus.CounterVec
	modelFailures  *prometheus.CounterVec
	speechFailures *prometheus.CounterVec
	toolCalls      *prometheus.CounterVec
	firstAudio     prometheus.Histogram
	appendErrors   *prometheus.CounterVec
	audioDropped   *prometheus.CounterVec
	blobBytes      prometheus.Counter
	blobDeclined   prometheus.Counter
	spansExported  prometheus.Counter
	spansDropped   *prometheus.CounterVec
	eventsDropped  prometheus.Counter

	mu      sync.Mutex
	calls   map[callKey]string
	order   []callKey
	watched bool
}

// callKey is a call id within its log: ids repeat across conversations.
type callKey struct{ conv, id string }

var _ journal.Observer = (*Metrics)(nil)

// NewMetrics registers every series, with the Go runtime's and the
// process's beside them.
func NewMetrics(version string) *Metrics {
	reg := prometheus.NewRegistry()
	f := func(c prometheus.Collector) { reg.MustRegister(c) }
	counter := func(name, help string) prometheus.Counter {
		c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
		f(c)
		return c
	}
	vec := func(name, help string, labels ...string) *prometheus.CounterVec {
		c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
		f(c)
		return c
	}
	m := &Metrics{
		reg:            reg,
		sessionsOpened: counter("chorus_sessions_opened_total", "Sessions opened, from session_opened."),
		sessionsClosed: vec("chorus_sessions_closed_total", "Sessions closed, by session_closed's reason.", "reason"),
		bargeDetected:  counter("chorus_barge_ins_detected_total", "Interruptions that passed the gate, from barge_in_detected."),
		bargeRejected:  vec("chorus_barge_ins_rejected_total", "Candidate interruptions refused, by the gate stage that refused them.", "stage"),
		modelFailures:  vec("chorus_model_failures_total", "Turns the model could not answer, by model_failed's reason.", "reason"),
		speechFailures: vec("chorus_speech_failures_total", "Speech that failed short of an interruption, by speech_failed's reason.", "reason"),
		toolCalls:      vec("chorus_tool_calls_total", "Tool results, by the tool called and the result's outcome.", "tool", "outcome"),
		appendErrors:   vec("chorus_journal_append_errors_total", "Journal writes that did not reach the log, by the kind they would have been.", "kind"),
		audioDropped:   vec("chorus_audio_dropped_total", "Audio recorded as gone, by audio_dropped's reason.", "reason"),
		blobBytes:      counter("chorus_blob_bytes_written_total", "Audio bytes committed to the blob store."),
		blobDeclined:   counter("chorus_blob_writes_declined_total", "Audio blobs not written because the disk was below its floor."),
		spansExported:  counter("chorus_otel_spans_exported_total", "Spans derived from the journal that the OTLP collector took."),
		spansDropped:   vec("chorus_otel_spans_dropped_total", "Spans derived from the journal and never exported, by why.", "reason"),
		eventsDropped:  counter("chorus_otel_events_dropped_total", "Journal events the trace export's queue was too full to take."),
		calls:          map[callKey]string{},
	}
	m.firstAudio = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "chorus_first_audio_seconds",
		Help:    "Wait from the person stopping speaking to the first played frame, from speech_started.",
		Buckets: firstAudioBuckets,
	})
	f(m.firstAudio)
	info := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "chorus_build_info", Help: "The running build.", ConstLabels: prometheus.Labels{"version": version},
	})
	info.Set(1)
	f(info)
	f(collectors.NewGoCollector())
	f(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

// Appended counts what one stored event records.
func (m *Metrics) Appended(e journal.Event) {
	switch e.Kind {
	case journal.KindSessionOpened:
		m.sessionsOpened.Inc()
	case journal.KindSessionClosed:
		m.sessionsClosed.WithLabelValues(e.Fields["reason"]).Inc()
	case journal.KindBargeInDetected:
		m.bargeDetected.Inc()
	case journal.KindBargeInRejected:
		m.bargeRejected.WithLabelValues(e.Fields["stage"]).Inc()
	case journal.KindModelFailed:
		m.modelFailures.WithLabelValues(e.Fields["reason"]).Inc()
	case journal.KindSpeechFailed:
		m.speechFailures.WithLabelValues(e.Fields["reason"]).Inc()
	case journal.KindAudioDropped:
		m.audioDropped.WithLabelValues(e.Fields["reason"]).Inc()
	case journal.KindSpeechStarted:
		// No wait recorded means the ask carried no stop time (ADR-0035).
		if ms, err := strconv.ParseFloat(e.Fields["wait_ms"], 64); err == nil {
			m.firstAudio.Observe(ms / 1000)
		}
	case journal.KindToolCalled:
		m.called(callKey{e.ConversationID, e.Fields["call_id"]}, e.Fields["tool"])
	case journal.KindToolResult:
		outcome := e.Fields["outcome"]
		m.toolCalls.WithLabelValues(m.resulted(callKey{e.ConversationID, e.Fields["call_id"]}, outcome), outcome).Inc()
	}
}

// AppendFailed counts a write that did not reach the log.
func (m *Metrics) AppendFailed(k journal.Kind, _ error) {
	m.appendErrors.WithLabelValues(string(k)).Inc()
}

func (m *Metrics) called(k callKey, tool string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.calls[k]; !ok {
		m.order = append(m.order, k)
	}
	m.calls[k] = tool
	for len(m.order) > maxOpenCalls {
		delete(m.calls, m.order[0])
		m.order = m.order[1:]
	}
}

// resulted names the call's tool. A timed-out call is kept: its detached
// work may still report (ADR-0060).
func (m *Metrics) resulted(k callKey, outcome string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	tool, ok := m.calls[k]
	if !ok {
		return "unknown"
	}
	if outcome != "timed_out" {
		delete(m.calls, k)
	}
	return tool
}

// SpansExported counts spans the collector took (otel.Meter, ADR-0104).
func (m *Metrics) SpansExported(n int) { m.spansExported.Add(float64(n)) }

// SpansDropped counts spans never exported, by why.
func (m *Metrics) SpansDropped(reason string, n int) {
	m.spansDropped.WithLabelValues(reason).Add(float64(n))
}

// EventsDropped counts events the trace export's queue was too full for.
func (m *Metrics) EventsDropped(n int) { m.eventsDropped.Add(float64(n)) }

// BlobDeclined counts one write the disk guard declined (blob.GuardConfig).
func (m *Metrics) BlobDeclined(string) { m.blobDeclined.Inc() }

// MeterBlobs wraps s so each committed blob's bytes are counted.
func (m *Metrics) MeterBlobs(s blob.Store) blob.Store {
	return &meteredStore{Store: s, bytes: m.blobBytes}
}

type meteredStore struct {
	blob.Store
	bytes prometheus.Counter
}

func (s *meteredStore) Create(ctx context.Context, key string) (blob.Writer, error) {
	w, err := s.Store.Create(ctx, key)
	if err != nil {
		return nil, err
	}
	return &meteredWriter{Writer: w, bytes: s.bytes}, nil
}

// Remove removes through, as retention and the disk guard expect.
func (s *meteredStore) Remove(ctx context.Context, ref string) error {
	if r, ok := s.Store.(interface {
		Remove(context.Context, string) error
	}); ok {
		return r.Remove(ctx, ref)
	}
	return nil
}

type meteredWriter struct {
	blob.Writer
	bytes   prometheus.Counter
	n       int
	counted bool
}

func (w *meteredWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += n
	return n, err
}

func (w *meteredWriter) Commit() (string, error) {
	ref, err := w.Writer.Commit()
	if err == nil && !w.counted {
		w.counted = true
		w.bytes.Add(float64(w.n))
	}
	return ref, err
}

// watch adds the gauges read from o's state at each scrape, once.
func (m *Metrics) watch(o *Ops) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watched {
		return
	}
	m.watched = true
	m.reg.MustRegister(stateCollector{o})
}

var (
	descSatsConnected = prometheus.NewDesc("chorus_satellites_connected", "Satellites whose audio link is connected.", nil, nil)
	descAudioLink     = prometheus.NewDesc("chorus_satellite_audio_link_connected", "1 while the satellite's audio link is connected.", []string{"satellite"}, nil)
	descNativeAPI     = prometheus.NewDesc("chorus_satellite_native_api_connected", "1 while the satellite's native API connection is held.", []string{"satellite"}, nil)
	descLastFrame     = prometheus.NewDesc("chorus_satellite_last_frame_timestamp_seconds", "When the satellite's audio link last delivered a frame.", []string{"satellite"}, nil)
	descDependencyUp  = prometheus.NewDesc("chorus_dependency_up", "1 when the last probe of the dependency answered.", []string{"dependency", "required"}, nil)
	descReady         = prometheus.NewDesc("chorus_ready", "1 when /readyz answers 200.", nil, nil)
)

// stateCollector reads readiness and the satellites at scrape time, so the
// gauges have no state of their own to drift.
type stateCollector struct{ o *Ops }

func (stateCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range []*prometheus.Desc{descSatsConnected, descAudioLink, descNativeAPI, descLastFrame, descDependencyUp, descReady} {
		ch <- d
	}
}

func (c stateCollector) Collect(ch chan<- prometheus.Metric) {
	r := c.o.Readiness()
	connected := 0
	for _, s := range r.Satellites {
		audio, native := bit(s.AudioLink == Connected), bit(s.NativeAPI == Connected)
		connected += int(audio)
		ch <- prometheus.MustNewConstMetric(descAudioLink, prometheus.GaugeValue, audio, s.Name)
		ch <- prometheus.MustNewConstMetric(descNativeAPI, prometheus.GaugeValue, native, s.Name)
		if s.LastFrameAt != nil {
			ch <- prometheus.MustNewConstMetric(descLastFrame, prometheus.GaugeValue, float64(s.LastFrameAt.UnixNano())/1e9, s.Name)
		}
	}
	ch <- prometheus.MustNewConstMetric(descSatsConnected, prometheus.GaugeValue, float64(connected))
	for _, d := range r.Dependencies {
		ch <- prometheus.MustNewConstMetric(descDependencyUp, prometheus.GaugeValue, bit(d.State == Up), d.Name, strconv.FormatBool(d.Required))
	}
	ch <- prometheus.MustNewConstMetric(descReady, prometheus.GaugeValue, bit(r.Status != StatusNotReady))
}

func bit(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
