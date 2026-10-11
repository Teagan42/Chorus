// Package ops is what an operator watches chorusd by: /healthz, /readyz and
// /metrics (SPEC §8, §13, ADR-0068). Readiness is probed on a loop with an
// injected clock and read from its last round, so no request waits on a
// dependency. Counters derive from the journal as it is written, through an
// observer on its append path, so they never disagree with the log
// (ADR-0007).
package ops

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Clock and Timers are the daemon's own seams, restated so this package
// needs neither the journal's nor the session's (CONTRIBUTING §1).
type (
	Clock  interface{ Now() time.Time }
	Timers interface {
		After(time.Duration) <-chan time.Time
	}
)

// DefaultInterval is how often every dependency is probed. DefaultTimeout
// bounds one probe: a sidecar slower than that is down for this round.
const (
	DefaultInterval = 15 * time.Second
	DefaultTimeout  = 3 * time.Second
)

// staleRounds is how many rounds the loop may miss before it is not alive.
const staleRounds = 3

// Config wires an Ops.
type Config struct {
	// Version is the build, reported as chorus_build_info.
	Version string

	Clock  Clock
	Timers Timers

	// Checks are the dependencies, in the order /readyz lists them.
	Checks []Check
	// Satellites is the inventory, in the order /readyz lists it.
	Satellites []Satellite

	// Interval and Timeout default to DefaultInterval and DefaultTimeout.
	Interval time.Duration
	Timeout  time.Duration

	// Metrics is the registry the journal and blob store already count
	// into. Nil makes a new one.
	Metrics *Metrics

	// Log hears each dependency go down and come back. Nil discards.
	Log *slog.Logger
}

// Ops holds the state the three endpoints read.
type Ops struct {
	clock    Clock
	interval time.Duration
	metrics  *Metrics
	ready    *readiness
	sats     *Satellites
}

// New composes the endpoints' state. Nothing is probed until Run.
func New(cfg Config) *Ops {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultInterval
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Metrics == nil {
		cfg.Metrics = NewMetrics(cfg.Version)
	}
	o := &Ops{
		clock:    cfg.Clock,
		interval: cfg.Interval,
		metrics:  cfg.Metrics,
		ready:    newReadiness(cfg),
		sats:     newSatellites(cfg.Clock, cfg.Satellites),
	}
	o.metrics.watch(o)
	return o
}

// Metrics is the registry the journal observer and blob meter count into.
func (o *Ops) Metrics() *Metrics { return o.metrics }

// Satellites is what the daemon tells about each satellite's links.
func (o *Ops) Satellites() *Satellites { return o.sats }

// Run probes every dependency now, then each interval, until ctx ends.
func (o *Ops) Run(ctx context.Context) { o.ready.run(ctx) }

// Handler serves the three endpoints and nothing else.
func (o *Ops) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", o.healthz)
	mux.HandleFunc("GET /readyz", o.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(o.metrics.reg, promhttp.HandlerOpts{}))
	return mux
}

// Status is the readiness verdict. Degraded still answers 200: what is
// down is optional, or a satellite that a restart would not bring back.
const (
	StatusReady    = "ready"
	StatusDegraded = "degraded"
	StatusNotReady = "not_ready"
)

// Readiness is the /readyz document.
type Readiness struct {
	Status       string           `json:"status"`
	CheckedAt    *time.Time       `json:"checked_at,omitempty"`
	Dependencies []Dependency     `json:"dependencies"`
	Satellites   []SatelliteState `json:"satellites"`
}

// Readiness is the verdict from the last round and the satellites now.
func (o *Ops) Readiness() Readiness {
	deps, beat := o.ready.snapshot()
	r := Readiness{Status: StatusReady, Dependencies: deps, Satellites: o.sats.states()}
	if !beat.IsZero() {
		r.CheckedAt = &beat
	}
	for _, d := range deps {
		switch {
		case d.State == Up:
		case d.Required:
			r.Status = StatusNotReady
			return r
		default:
			r.Status = StatusDegraded
		}
	}
	for _, s := range r.Satellites {
		if s.AudioLink != Connected || s.NativeAPI != Connected {
			r.Status = StatusDegraded
		}
	}
	return r
}

func (o *Ops) readyz(w http.ResponseWriter, _ *http.Request) {
	r := o.Readiness()
	code := http.StatusOK
	if r.Status == StatusNotReady {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, r)
}

// health is the /healthz document.
type health struct {
	Status    string     `json:"status"`
	LastRound *time.Time `json:"last_round,omitempty"`
}

// healthz calls nothing: the process answering is half of it, and the probe
// loop having gone round within staleRounds intervals is the other.
func (o *Ops) healthz(w http.ResponseWriter, _ *http.Request) {
	_, beat := o.ready.snapshot()
	switch {
	case beat.IsZero():
		writeJSON(w, http.StatusServiceUnavailable, health{Status: "starting"})
	case o.clock.Now().Sub(beat) > staleRounds*o.interval:
		writeJSON(w, http.StatusServiceUnavailable, health{Status: "stalled", LastRound: &beat})
	default:
		writeJSON(w, http.StatusOK, health{Status: "ok", LastRound: &beat})
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
