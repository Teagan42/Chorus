package ops

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// State is what the last round found a dependency to be.
type State string

const (
	// Unknown is a dependency no round has answered for yet.
	Unknown State = "unknown"
	Up      State = "up"
	Down    State = "down"
)

// Check is one dependency and how to ask whether it is up.
type Check struct {
	Name string
	// Required dependencies fail readiness when down. The rest degrade it,
	// as chorusd already starts without them.
	Required bool
	// Probe returns nil when the dependency answers. It must honour ctx.
	Probe func(context.Context) error
}

// Dependency is one check as /readyz reports it.
type Dependency struct {
	Name     string `json:"name"`
	Required bool   `json:"required"`
	State    State  `json:"state"`
	// Since is when it entered this state, CheckedAt when it was last asked.
	Since     *time.Time `json:"since,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
	Error     string     `json:"error,omitempty"`
}

// readiness runs the probe loop and keeps each check's last answer.
type readiness struct {
	checks   []Check
	clock    Clock
	timers   Timers
	interval time.Duration
	timeout  time.Duration
	log      *slog.Logger

	mu    sync.Mutex
	beat  time.Time
	state []Dependency
}

func newReadiness(cfg Config) *readiness {
	r := &readiness{
		checks: cfg.Checks, clock: cfg.Clock, timers: cfg.Timers,
		interval: cfg.Interval, timeout: cfg.Timeout, log: cfg.Log,
		state: make([]Dependency, len(cfg.Checks)),
	}
	for i, c := range cfg.Checks {
		r.state[i] = Dependency{Name: c.Name, Required: c.Required, State: Unknown}
	}
	return r
}

func (r *readiness) run(ctx context.Context) {
	for {
		r.round(ctx)
		select {
		case <-ctx.Done():
			return
		case <-r.timers.After(r.interval):
		}
	}
}

// round probes every check at once and waits for each, bounded by timeout.
func (r *readiness) round(ctx context.Context) {
	r.mu.Lock()
	r.beat = r.clock.Now()
	r.mu.Unlock()
	var wg sync.WaitGroup
	for i := range r.checks {
		wg.Go(func() {
			err := r.probe(ctx, r.checks[i])
			if ctx.Err() == nil {
				r.record(i, err)
			}
		})
	}
	wg.Wait()
}

// probe asks one check, giving up at the timeout on the injected timers. A
// probe that ignores its cancel is left to finish alone; the round does not
// wait for it.
func (r *readiness) probe(ctx context.Context, c Check) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	res := make(chan error, 1)
	go func() { res <- c.Probe(ctx) }()
	select {
	case err := <-res:
		return err
	case <-r.timers.After(r.timeout):
		return fmt.Errorf("no answer within %v", r.timeout)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *readiness) record(i int, err error) {
	now := r.clock.Now()
	r.mu.Lock()
	d := &r.state[i]
	was := d.State
	d.State, d.Error, d.CheckedAt = Up, "", &now
	if err != nil {
		d.State, d.Error = Down, err.Error()
	}
	if d.State != was {
		d.Since = &now
	}
	cur := *d
	r.mu.Unlock()

	switch {
	case cur.State == was:
	case cur.State == Down:
		r.log.Warn("dependency down", "dependency", cur.Name, "required", cur.Required, "err", cur.Error)
	case was == Down:
		r.log.Info("dependency up again", "dependency", cur.Name, "required", cur.Required)
	}
}

// snapshot is every check's last answer and when the last round began.
func (r *readiness) snapshot() ([]Dependency, time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Dependency(nil), r.state...), r.beat
}
