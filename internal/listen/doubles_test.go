package listen_test

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/bridge/bridgetest"
	"github.com/teaganglenn/chorus/internal/identity"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/listen"
	"github.com/teaganglenn/chorus/internal/session"
	"github.com/teaganglenn/chorus/internal/stt"
)

// patience bounds a wait that only expires when the implementation is wrong.
// Nothing in a passing run waits on it.
const patience = 5 * time.Second

// A test's audio carries its own script: every line spoken gets an amplitude
// of its own, and both the STT and the embedder read the peak sample. So the
// text and the voice of an utterance are properties of its audio, not of
// when a decode happened to run.
const firstAmplitude int16 = 8000

// dim keeps a vector readable. The package never assumes 192 (ADR-0025).
const dim = 4

// Voices. alan is enrolled; the stranger is the television.
var (
	alan     = axis(0)
	stranger = axis(3)
)

func axis(i int) []float32 {
	v := make([]float32, dim)
	v[i] = 1
	return v
}

// ---------------------------------------------------------------- the model

// step is one action the model emits. Replicates the shape of the session
// package's own double, which is not importable (internal/session/doubles_test.go).
type step struct{ act session.Action }

// scriptEngine replays a fixed action stream on every turn and records what
// it was asked.
type scriptEngine struct {
	steps []step

	mu     sync.Mutex
	inputs []session.Input
}

func (e *scriptEngine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, in)
	e.mu.Unlock()

	out := make(chan session.Action)
	go func() {
		defer close(out)
		for _, s := range e.steps {
			select {
			case out <- s.act:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

func (e *scriptEngine) heard() []session.Input {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]session.Input(nil), e.inputs...)
}

// -------------------------------------------------------------- the speaker

// fakeSpeaker plays text instantly unless held. Held playback is how a test
// keeps the Speaking child alive while it provokes a barge-in; the DAC
// position itself comes from the device, not from here.
type fakeSpeaker struct {
	hold bool

	written chan string
	release chan struct{}
	once    sync.Once
}

func newSpeaker() *fakeSpeaker {
	return &fakeSpeaker{written: make(chan string, 64), release: make(chan struct{})}
}

func (f *fakeSpeaker) Open(ctx context.Context, callID string) (session.Stream, error) {
	return &fakeStream{sp: f, ctx: ctx, callID: callID}, nil
}

// wrote blocks until the session has started playing an utterance.
func (f *fakeSpeaker) wrote(t *testing.T) string {
	t.Helper()
	select {
	case s := <-f.written:
		return s
	case <-time.After(patience):
		t.Fatal("speaker was never written to")
		return ""
	}
}

func (f *fakeSpeaker) let() { f.once.Do(func() { close(f.release) }) }

type fakeStream struct {
	sp     *fakeSpeaker
	ctx    context.Context
	callID string
	text   string
}

func (s *fakeStream) Write(text string) error {
	s.text += text
	s.sp.written <- text
	return nil
}

func (s *fakeStream) Close() session.Playback {
	if s.sp.hold {
		select {
		case <-s.sp.release:
		case <-s.ctx.Done():
			cut := min(3, len(s.text))
			return session.Playback{
				Spoken: s.text[:cut], Unspoken: s.text[cut:], Frames: int64(cut) * 160,
				Truncated: true, AudioRef: "blob://tts/" + s.callID,
			}
		}
	}
	return session.Playback{Spoken: s.text, Frames: int64(len(s.text)) * 160, AudioRef: "blob://tts/" + s.callID}
}

// fakePlayback stands in for the Speaking side's origin: where on the DAC's
// cumulative count the speech now playing began. *satellite.Satellite is the
// real one.
type fakePlayback struct {
	mu   sync.Mutex
	base uint64
}

func (f *fakePlayback) SpeechBase() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.base
}

func (f *fakePlayback) rebase(frames uint64) {
	f.mu.Lock()
	f.base = frames
	f.mu.Unlock()
}

// neverTimers never fires: no silence backstop and no tool timeout can end a
// session behind a test's back.
type neverTimers struct{}

func (neverTimers) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// ------------------------------------------------------------------- the STT

// fakeSTT answers a decode with the text scripted for the audio's peak
// sample, and can hold a decode open so a test can cancel around it. Audio
// nobody scripted decodes to nothing, as noise does.
type fakeSTT struct {
	mu    sync.Mutex
	by    map[int16]string
	hold  chan struct{} // nil answers at once
	calls [][]byte
	// held reports a decode that is parked, with the context it is parked on.
	held chan context.Context
}

func newSTT() *fakeSTT {
	return &fakeSTT{by: map[int16]string{}, held: make(chan context.Context, 16)}
}

func (f *fakeSTT) Transcribe(ctx context.Context, pcm []byte) (stt.Result, error) {
	f.mu.Lock()
	text, hold := f.by[peak(pcm)], f.hold
	f.calls = append(f.calls, pcm)
	f.mu.Unlock()
	if hold != nil {
		f.held <- ctx
		select {
		case <-hold:
		case <-ctx.Done():
			return stt.Result{}, ctx.Err()
		}
	}
	return stt.Result{Text: text}, nil
}

// park makes every later decode wait until the returned function is called.
func (f *fakeSTT) park() func() {
	hold := make(chan struct{})
	f.mu.Lock()
	f.hold = hold
	f.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { close(hold) }) }
}

func (f *fakeSTT) decodes() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// parked waits for a decode to be held and returns the context it is on.
func (f *fakeSTT) parked(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-f.held:
		return ctx
	case <-time.After(patience):
		t.Fatal("no decode was parked")
		return nil
	}
}

// -------------------------------------------------------------- the embedder

// fakeEmbedder maps the peak sample of an utterance to the vector scripted
// for that voice. Silence, or a voice nobody scripted, is the sidecar being
// down.
type fakeEmbedder struct {
	mu    sync.Mutex
	by    map[int16][]float32
	calls int
}

func newEmbedder() *fakeEmbedder {
	return &fakeEmbedder{by: map[int16][]float32{}}
}

func (f *fakeEmbedder) Embed(_ context.Context, pcm []byte) ([]float32, error) {
	f.mu.Lock()
	f.calls++
	v, ok := f.by[peak(pcm)]
	f.mu.Unlock()
	if !ok {
		return nil, errors.New("sidecar unreachable")
	}
	return v, nil
}

func (f *fakeEmbedder) Model() string { return "fake" }
func (f *fakeEmbedder) Dim() int      { return dim }

func (f *fakeEmbedder) embeds() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// peak is the loudest sample, which is the amplitude voice() wrote.
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

// household enrolls alan and nobody else. A single centroid is enough: the
// stranger is orthogonal to it, and alan's own takes are its axis.
func household(t *testing.T, emb identity.Embedder) *identity.Resolver {
	t.Helper()
	ids := identity.New(emb)
	takes := [][]float32{axis(0), axis(0), axis(0)}
	if err := ids.Enroll("alan", "Alan", takes); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	r, err := identity.NewResolver(emb, ids, identity.Thresholds{})
	if err != nil {
		t.Fatalf("resolver: %v", err)
	}
	return r
}

// ------------------------------------------------------------------ the rig

// rig is a listener on a real link to an in-process satellite, feeding a real
// supervisor over in-memory doubles. Every dependency that reads time or does
// I/O is injected (CONTRIBUTING §1).
type rig struct {
	link    *bridge.Link
	dev     *bridgetest.Device
	store   *journal.MemStore
	blobs   *blob.Memory
	engine  *scriptEngine
	speaker *fakeSpeaker
	stt     *fakeSTT
	emb     *fakeEmbedder
	l       *listen.Listener
	cancel  context.CancelFunc
	served  chan error
	lines   int16
}

// Rig bounds, small so a test speaks for milliseconds rather than seconds.
const (
	rigSilence  = 4 * chunkBytes
	rigPartials = 8 * chunkBytes
	rigWake     = 16 * chunkBytes
)

func versions() journal.Versions {
	return journal.Versions{Model: "qwen3-32b", Prompt: "p1", ToolSchema: "t1"}
}

func newRig(t *testing.T, steps []step, tweak ...func(*listen.Config)) *rig {
	t.Helper()
	link, dev := bridgetest.Dial(t, 2)
	store := journal.NewMemStore()
	clk := journal.FixedClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	jnl := journal.New(store, clk, versions())
	engine := &scriptEngine{steps: steps}
	speaker := newSpeaker()
	sup, err := session.New(session.Config{
		Journal: jnl, Store: store, Clock: clk, Timers: neverTimers{},
		Engine: engine, Speaker: speaker,
		Conversations: session.NewConversations(clk, session.MigrationWindow),
		Gate:          session.Gate{MinEnergy: 0.1, MinWords: 2, Household: []string{"alan"}},
	})
	if err != nil {
		t.Fatalf("supervisor: %v", err)
	}
	r := &rig{
		link: link, dev: dev, store: store, blobs: blob.NewMemory(),
		engine: engine, speaker: speaker, stt: newSTT(), emb: newEmbedder(),
		served: make(chan error, 1),
	}
	cfg := listen.Config{
		Satellite:   "kitchen",
		Sessions:    sup,
		Transcriber: r.stt,
		Speakers:    household(t, r.emb),
		Blobs:       r.blobs,
		Journal:     jnl,
		Endpointer:  &listen.Energy{Threshold: 0.05, Silence: rigSilence},
		STT:         stt.Options{PartialEvery: rigPartials},
		WakeWindow:  rigWake,
		LeadIn:      chunkBytes,
	}
	for _, f := range tweak {
		f(&cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	l, err := listen.Open(ctx, cfg)
	if err != nil {
		t.Fatalf("open listener: %v", err)
	}
	r.l = l
	go func() { r.served <- link.Serve(ctx, l) }()

	t.Cleanup(func() {
		speaker.let()
		cancel()
		select {
		case <-l.Done():
		case <-time.After(patience):
			t.Error("the listener's goroutines outlived its context")
		}
		select {
		case <-r.served:
		case <-time.After(patience):
			t.Error("Serve did not return on cancel")
		}
	})
	return r
}

// line scripts what a voice says and returns the amplitude that speaks it.
// Loud enough for every threshold in the rig, whoever the speaker is: the
// television is loud too, and the gate must reject it on identity alone.
func (r *rig) line(text string, who []float32) int16 {
	amp := firstAmplitude + r.lines
	r.lines++
	r.stt.mu.Lock()
	r.stt.by[amp] = text
	r.stt.mu.Unlock()
	r.emb.mu.Lock()
	r.emb.by[amp] = who
	r.emb.mu.Unlock()
	return amp
}

// speak streams n bytes of a voice in uplink chunks on the AEC channel.
func (r *rig) speak(t *testing.T, amplitude int16, n int) {
	t.Helper()
	for sent := 0; sent < n; sent += chunkBytes {
		r.dev.SendMic(t, bridge.ChannelAEC, voice(amplitude, min(chunkBytes, n-sent)))
	}
}

// pause streams n bytes of silence.
func (r *rig) pause(t *testing.T, n int) {
	t.Helper()
	for sent := 0; sent < n; sent += chunkBytes {
		r.dev.SendMic(t, bridge.ChannelAEC, quiet(min(chunkBytes, n-sent)))
	}
}

// utter is a whole utterance: speech, then enough silence to end it.
func (r *rig) utter(t *testing.T, amplitude int16, n int) {
	t.Helper()
	r.speak(t, amplitude, n)
	r.pause(t, rigSilence)
}

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

// session waits for the listener to have opened one.
func (r *rig) session(t *testing.T) *session.Session {
	t.Helper()
	await(t, "a session to open", func() bool { return r.l.Session() != nil })
	return r.l.Session()
}

func (r *rig) events(t *testing.T, convID string) []journal.Event {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return events
}

func (r *rig) count(t *testing.T, convID string, k journal.Kind) int {
	t.Helper()
	n := 0
	for _, e := range r.events(t, convID) {
		if e.Kind == k {
			n++
		}
	}
	return n
}

// awaitKind waits for the nth event of a kind and returns it.
func (r *rig) awaitKind(t *testing.T, convID string, k journal.Kind, n int) journal.Event {
	t.Helper()
	await(t, fmt.Sprintf("%d %s event(s)", n, k), func() bool { return r.count(t, convID, k) >= n })
	var found []journal.Event
	for _, e := range r.events(t, convID) {
		if e.Kind == k {
			found = append(found, e)
		}
	}
	return found[n-1]
}

// settled waits for the device to have delivered everything the test sent:
// the next frame it reads proves the earlier ones were dispatched.
func (r *rig) settled(t *testing.T) {
	t.Helper()
	r.dev.SendMic(t, bridge.ChannelRaw, quiet(2))
}
