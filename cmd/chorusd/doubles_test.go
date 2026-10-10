package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

// patience bounds a wait that only expires when the implementation is wrong.
// Nothing in a passing run waits on it.
const patience = 5 * time.Second

// chunkBytes is one 32 ms uplink chunk, the firmware's own send size.
const chunkBytes = 1024

// dim keeps a vector readable; nothing here assumes 192 (ADR-0025).
const dim = 4

// await spins until cond holds. Driven by progress the implementation is
// already committed to, so a timeout means it is wrong rather than slow.
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

// ----------------------------------------------------------------- audio

// voice is n bytes of a constant sample. A test's "voice" is an amplitude:
// the scripted STT and embedder read the peak sample, so text and speaker
// are properties of the audio and not of when a decode happened to run.
func voice(amplitude int16, n int) []byte {
	out := make([]byte, n)
	for i := 0; i+1 < n; i += 2 {
		binary.LittleEndian.PutUint16(out[i:], uint16(amplitude))
	}
	return out
}

func quiet(n int) []byte { return make([]byte, n) }

func peak(pcm []byte) int16 {
	var best int16
	for i := 0; i+1 < len(pcm); i += 2 {
		s := int16(binary.LittleEndian.Uint16(pcm[i:]))
		if abs(s) > abs(best) {
			best = s
		}
	}
	return best
}

func abs(s int16) int32 {
	if s < 0 {
		return -int32(s)
	}
	return int32(s)
}

// -------------------------------------------------------------- listener

// memListener is the daemon's accept side with no socket: a test hands it
// connections whose remote address it chose, which is the one fact the
// daemon identifies a satellite by.
type memListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newListener() *memListener {
	return &memListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *memListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4zero, Port: 6055} }

// dial delivers a connection from ip to the daemon and returns the device's
// end of it.
func (l *memListener) dial(t *testing.T, ip string) net.Conn {
	t.Helper()
	host, device := net.Pipe()
	t.Cleanup(func() {
		_ = host.Close()
		_ = device.Close()
	})
	from := &addressed{Conn: host, remote: &net.TCPAddr{IP: net.ParseIP(ip), Port: 49152}}
	select {
	case l.conns <- from:
	case <-l.done:
		t.Fatal("the daemon stopped accepting")
	case <-time.After(patience):
		t.Fatal("the daemon never accepted the connection")
	}
	return device
}

// addressed gives a pipe end the remote address a TCP connection would have.
type addressed struct {
	net.Conn
	remote net.Addr
}

func (a *addressed) RemoteAddr() net.Addr { return a.remote }

// ---------------------------------------------------------------- journal

// spyStore records every event as it lands, across all conversations, so a
// test can find a session it has no handle to.
type spyStore struct {
	journal.Store

	mu  sync.Mutex
	all []journal.Event
}

func newSpyStore() *spyStore { return &spyStore{Store: journal.NewMemStore()} }

func (s *spyStore) Append(ctx context.Context, e journal.Event) error {
	if err := s.Store.Append(ctx, e); err != nil {
		return err
	}
	s.mu.Lock()
	s.all = append(s.all, e)
	s.mu.Unlock()
	return nil
}

func (s *spyStore) events() []journal.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]journal.Event(nil), s.all...)
}

func (s *spyStore) ofKind(k journal.Kind) []journal.Event {
	var out []journal.Event
	for _, e := range s.events() {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// awaitKind waits for the nth event of a kind in any conversation.
func (s *spyStore) awaitKind(t *testing.T, k journal.Kind, n int) journal.Event {
	t.Helper()
	await(t, string(k)+" event", func() bool { return len(s.ofKind(k)) >= n })
	return s.ofKind(k)[n-1]
}

// ------------------------------------------------------------------ model

// scriptEngine replays a fixed action stream to every utterance and records
// what it was asked. The same shape as the session package's own double.
type scriptEngine struct {
	acts []session.Action

	// answer is how it speaks to a follow-up ask, given the result the
	// utterance led to. Nil says nothing.
	answer func(journal.Entry) []session.Action

	// decide, when set, answers every ask from the dialogue it carries, as a
	// model that has to remember an earlier turn would.
	decide func(session.Input) []session.Action

	mu     sync.Mutex
	inputs []session.Input
}

func (e *scriptEngine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, in)
	e.mu.Unlock()
	acts := e.acts
	if r, ok := lastResult(in); ok {
		acts = nil
		if e.answer != nil {
			acts = e.answer(r)
		}
	}
	if e.decide != nil {
		acts = e.decide(in)
	}
	out := make(chan session.Action)
	go func() {
		defer close(out)
		for _, a := range acts {
			select {
			case out <- a:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// lastResult is the newest result the utterance being answered led to. An
// ask that has one is a follow-up, not the answer to the utterance itself.
func lastResult(in session.Input) (journal.Entry, bool) {
	for i := len(in.Dialogue) - 1; i >= 0; i-- {
		switch in.Dialogue[i].Kind {
		case journal.EntryHeard:
			return journal.Entry{}, false
		case journal.EntryResult:
			return in.Dialogue[i], true
		}
	}
	return journal.Entry{}, false
}

func (e *scriptEngine) heard() []session.Input {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]session.Input(nil), e.inputs...)
}

// silentSynth renders every clause as a short silence. The scripted model
// never speaks in these tests, so it is only here to satisfy the wiring.
type silentSynth struct{}

func (silentSynth) Synthesize(_ context.Context, text string) ([]byte, error) {
	return quiet(2 * len(text)), nil
}

// neverTimers never fires: no silence backstop, drain or retry can end
// anything behind a test's back.
type neverTimers struct{}

func (neverTimers) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// ----------------------------------------------------------------- hearing

// fakeSTT answers a decode with the text scripted for the audio's peak
// sample. Unscripted audio decodes to nothing, as noise does.
type fakeSTT struct {
	mu sync.Mutex
	by map[int16]string
}

func newSTT() *fakeSTT { return &fakeSTT{by: map[int16]string{}} }

func (f *fakeSTT) Transcribe(_ context.Context, pcm []byte) (stt.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return stt.Result{Text: f.by[peak(pcm)]}, nil
}

// fakeEmbedder maps the peak sample to the vector scripted for that voice.
type fakeEmbedder struct {
	mu sync.Mutex
	by map[int16][]float32
}

func newEmbedder() *fakeEmbedder { return &fakeEmbedder{by: map[int16][]float32{}} }

func (f *fakeEmbedder) Embed(_ context.Context, pcm []byte) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.by[peak(pcm)]
	if !ok {
		return nil, errors.New("sidecar unreachable")
	}
	return v, nil
}

func (f *fakeEmbedder) Model() string { return "fake" }
func (f *fakeEmbedder) Dim() int      { return dim }

func axis(i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

// household enrolls alan and nobody else.
func household(t *testing.T, emb identity.Embedder) *identity.Resolver {
	t.Helper()
	ids := identity.New(emb)
	if err := ids.Enroll("alan", "Alan", [][]float32{axis(0), axis(0), axis(0)}); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	r, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	return r
}

// ------------------------------------------------------------------- logs

// logBuffer is a goroutine-safe sink for the daemon's slog output.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func (l *logBuffer) await(t *testing.T, substr string) {
	t.Helper()
	await(t, "a log line containing "+substr, func() bool { return strings.Contains(l.String(), substr) })
}

// ------------------------------------------------------------- native api

// fakeDialer hands out whatever the test scripted, one result per attempt,
// and records what it was asked to dial.
type fakeDialer struct {
	results chan dialResult

	mu       sync.Mutex
	attempts []string
}

type dialResult struct {
	conn nativeConn
	err  error
}

func newDialer() *fakeDialer { return &fakeDialer{results: make(chan dialResult, 8)} }

func (d *fakeDialer) Dial(ctx context.Context, address, psk string) (nativeConn, error) {
	d.mu.Lock()
	d.attempts = append(d.attempts, address+" "+psk)
	d.mu.Unlock()
	select {
	case r := <-d.results:
		return r.conn, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (d *fakeDialer) dialed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.attempts...)
}

// fakeNative is one held native API connection: the test feeds it messages
// the device would send and sees what the daemon answered.
type fakeNative struct {
	in     chan proto.Message
	sent   chan proto.Message
	closed chan struct{}
	once   sync.Once
}

func newNative() *fakeNative {
	return &fakeNative{in: make(chan proto.Message, 8), sent: make(chan proto.Message, 8), closed: make(chan struct{})}
}

func (c *fakeNative) Describe() string { return "satellite1 esphome 2026.7.2" }

func (c *fakeNative) Send(m proto.Message) error {
	select {
	case c.sent <- m:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *fakeNative) Recv() (proto.Message, error) {
	select {
	case m := <-c.in:
		return m, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *fakeNative) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// sent waits for the daemon's next message to the device, which must be a T.
func sent[T proto.Message](t *testing.T, c *fakeNative, what string) T {
	t.Helper()
	select {
	case m := <-c.sent:
		got, ok := m.(T)
		if !ok {
			t.Fatalf("waiting for %s, the daemon sent %T", what, m)
		}
		return got
	case <-time.After(patience):
		t.Fatalf("the daemon never sent %s", what)
	}
	var zero T
	return zero
}

// ------------------------------------------------------------------ clock

var epoch = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// clock is a virtual clock with deadline-ordered timers, for the tests that
// drive a retry.
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

func newClock() *clock { return &clock{now: epoch} }

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

// advance moves the clock and expires every timer that came due.
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

// awaitWait blocks until a timer of exactly this delay is armed.
func (c *clock) awaitWait(t *testing.T, d time.Duration) {
	t.Helper()
	await(t, "a "+d.String()+" timer", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		for _, tm := range c.timers {
			if tm.waited == d {
				return true
			}
		}
		return false
	})
}

// turnJudge says every turn is finished, as Smart Turn does for "turn off
// the kitchen lights", and counts what it was asked.
type turnJudge struct {
	// gate, when set, holds the first verdict until the test closes it: a
	// sidecar slower than the audio arriving.
	gate chan struct{}

	mu    sync.Mutex
	asked int
}

func (j *turnJudge) Complete(ctx context.Context, _ []byte) (bool, error) {
	j.mu.Lock()
	j.asked++
	first := j.asked == 1
	j.mu.Unlock()
	if first && j.gate != nil {
		select {
		case <-j.gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return true, nil
}

func (j *turnJudge) asks() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.asked
}
