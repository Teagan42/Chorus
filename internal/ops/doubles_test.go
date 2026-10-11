package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// patience bounds a wait that only expires when the implementation is wrong.
const patience = 5 * time.Second

// await spins until cond holds; a timeout means the code is wrong, not slow.
func await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Seven forty-two on a Thursday morning in the house the fixtures live in.
var breakfast = time.Date(2026, 10, 8, 7, 42, 0, 0, time.UTC)

// clock is a virtual clock whose timers fire only when the test advances it.
type clock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*vtimer
}

type vtimer struct {
	deadline time.Time
	waited   time.Duration
	ch       chan time.Time
}

func newClock() *clock { return &clock{now: breakfast} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &vtimer{deadline: c.now.Add(d), waited: d, ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// advance moves the clock and fires every timer that came due.
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due, live []*vtimer
	for _, t := range c.timers {
		if t.deadline.After(c.now) {
			live = append(live, t)
			continue
		}
		due = append(due, t)
	}
	c.timers = live
	c.mu.Unlock()
	for _, t := range due {
		t.ch <- t.deadline
	}
}

// armed is how many timers of exactly d are waiting.
func (c *clock) armed(d time.Duration) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if t.waited == d {
			n++
		}
	}
	return n
}

// sidecar is a dependency a test switches off and on.
type sidecar struct {
	mu   sync.Mutex
	err  error
	hang bool
	n    int
}

func (s *sidecar) set(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *sidecar) probe(ctx context.Context) error {
	s.mu.Lock()
	s.n++
	err, hang := s.err, s.hang
	s.mu.Unlock()
	if hang {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (s *sidecar) probes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.n
}

var errRefused = errors.New("dial tcp 10.0.0.20:8000: connection refused")

// get serves one request through h, with no socket.
func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// readyDoc is /readyz as a reader decodes it.
type readyDoc struct {
	Status       string `json:"status"`
	Dependencies []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
		State    string `json:"state"`
		Error    string `json:"error"`
	} `json:"dependencies"`
	Satellites []struct {
		Name        string     `json:"name"`
		Room        string     `json:"room"`
		AudioLink   string     `json:"audio_link"`
		NativeAPI   string     `json:"native_api"`
		LastFrameAt *time.Time `json:"last_frame_at"`
	} `json:"satellites"`
}

func ready(t *testing.T, h http.Handler) (int, readyDoc) {
	t.Helper()
	rec := get(t, h, "/readyz")
	var doc readyDoc
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("/readyz is not JSON: %v\n%s", err, rec.Body.String())
	}
	return rec.Code, doc
}

func (d readyDoc) state(name string) string {
	for _, dep := range d.Dependencies {
		if dep.Name == name {
			return dep.State
		}
	}
	return "absent"
}

// sample is the value of one series in a Prometheus text scrape, or -1.
func sample(t *testing.T, h http.Handler, series string) float64 {
	t.Helper()
	body := get(t, h, "/metrics").Body.String()
	for line := range strings.SplitSeq(body, "\n") {
		name, value, ok := strings.Cut(line, " ")
		if !ok || name != series {
			continue
		}
		v, err := strconv.ParseFloat(value, 64)
		if err != nil {
			t.Fatalf("%s = %q: %v", series, value, err)
		}
		return v
	}
	return -1
}
