package session_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/registry"
	"github.com/teaganglenn/chorus/internal/session"
)

// patience bounds a wait that only expires when the implementation is wrong.
// Nothing in a passing run waits on it.
const patience = 2 * time.Second

// ---------------------------------------------------------------- turn engine

// step is one action the model emits, plus an optional gate that must open
// before the next action is emitted. The gate is how a test proves an action
// was dispatched mid-stream rather than at end of message.
type step struct {
	act  session.Action
	gate <-chan struct{}
}

// scriptEngine replays a fixed action stream. It records a stall instead of
// hanging, so a serialising orchestrator fails with a readable message.
type scriptEngine struct {
	steps []step

	mu     sync.Mutex
	stall  string
	inputs []session.Input
}

func (e *scriptEngine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	e.mu.Lock()
	e.inputs = append(e.inputs, in)
	e.mu.Unlock()

	out := make(chan session.Action)
	go func() {
		defer close(out)
		for i, s := range e.steps {
			select {
			case out <- s.act:
			case <-ctx.Done():
				return
			}
			if s.gate == nil {
				continue
			}
			select {
			case <-s.gate:
			case <-ctx.Done():
				return
			case <-time.After(patience):
				e.mu.Lock()
				e.stall = fmt.Sprintf("step %d (%T) was never dispatched", i, s.act)
				e.mu.Unlock()
				return
			}
		}
	}()
	return out, nil
}

func (e *scriptEngine) stalled() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stall
}

// ------------------------------------------------------------------- speaker

// fakeSpeaker plays text instantly unless held. Held playback is how a test
// positions a barge-in in the middle of an utterance.
type fakeSpeaker struct {
	hold bool

	// cut is how many bytes of a held utterance were played before the cut.
	cut int

	mu      sync.Mutex
	opened  []string
	written chan string
	release chan struct{}
}

func newSpeaker() *fakeSpeaker {
	return &fakeSpeaker{written: make(chan string, 64), release: make(chan struct{})}
}

func (f *fakeSpeaker) Open(ctx context.Context, callID string) (session.Stream, error) {
	f.mu.Lock()
	f.opened = append(f.opened, callID)
	f.mu.Unlock()
	return &fakeStream{sp: f, ctx: ctx, callID: callID}, nil
}

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
			cut := min(s.sp.cut, len(s.text))
			return session.Playback{
				Spoken:    s.text[:cut],
				Unspoken:  s.text[cut:],
				Frames:    int64(cut) * 160,
				Truncated: true,
				AudioRef:  "blob://tts/" + s.callID,
			}
		}
	}
	return session.Playback{
		Spoken:   s.text,
		Frames:   int64(len(s.text)) * 160,
		AudioRef: "blob://tts/" + s.callID,
	}
}

// ---------------------------------------------------------------------- tools

// gateTool blocks until released, so a test can hold a call in flight across
// a barge-in and inspect the interrupt policy that applied to it.
type gateTool struct {
	result  string
	release chan struct{}

	mu      sync.Mutex
	entered chan struct{}
	done    bool
}

func newGateTool(result string) *gateTool {
	return &gateTool{result: result, release: make(chan struct{}), entered: make(chan struct{}, 1)}
}

func (g *gateTool) Invoke(ctx context.Context, _ string) (string, error) {
	g.entered <- struct{}{}
	select {
	case <-g.release:
		g.mu.Lock()
		g.done = true
		g.mu.Unlock()
		return g.result, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (g *gateTool) enter(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(patience):
		t.Fatal("tool was never invoked")
	}
}

// ------------------------------------------------------------------- fixtures

func versions() journal.Versions {
	return journal.Versions{Model: "qwen3-32b", Prompt: "p1", ToolSchema: "t1"}
}

// rig wires a supervisor against in-memory doubles. Every dependency that
// reads time or does I/O is injected (CONTRIBUTING §1).
type rig struct {
	sup     *session.Supervisor
	store   *journal.MemStore
	clock   *clock
	engine  *scriptEngine
	speaker *fakeSpeaker
	tools   map[string]session.Tool
}

func newRig(t *testing.T, steps []step, tools map[string]session.Tool) *rig {
	t.Helper()
	return newRigSpecs(t, steps, tools, nil)
}

// newRigSpecs overrides the tool registry, so a test can exercise a policy
// combination no shipped tool declares yet.
func newRigSpecs(t *testing.T, steps []step, tools map[string]session.Tool, specs map[string]registry.ToolSpec) *rig {
	t.Helper()

	store := journal.NewMemStore()
	clk := newClock()
	eng := &scriptEngine{steps: steps}
	sp := newSpeaker()
	if tools == nil {
		tools = map[string]session.Tool{}
	}
	sup, err := session.New(session.Config{
		Journal:       journal.New(store, clk, versions()),
		Store:         store,
		Clock:         clk,
		Timers:        clk,
		Engine:        eng,
		Speaker:       sp,
		Tools:         tools,
		Specs:         specs,
		Conversations: session.NewConversations(clk, session.MigrationWindow),
		Gate:          session.Gate{MinEnergy: 0.2, MinWords: 2, Household: []string{"alice", "bob"}},
	})
	if err != nil {
		t.Fatalf("new supervisor: %v", err)
	}
	return &rig{sup: sup, store: store, clock: clk, engine: eng, speaker: sp, tools: tools}
}

// state replays the log, which is the only legitimate way to read a session:
// the supervisor keeps no authoritative copy (SPEC §8).
func (r *rig) state(t *testing.T, convID string) journal.State {
	t.Helper()
	st, err := journal.Replay(context.Background(), r.store, convID, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return st
}

func (r *rig) open(t *testing.T, person string) *session.Session {
	t.Helper()
	s, err := r.sup.Open(context.Background(), session.Wake{Satellite: "kitchen", PersonID: person})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

// heard runs a turn in the background and returns a channel carrying its
// error, so the test can act while the turn's children are still alive.
func heard(s *session.Session, text string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{Text: text, AudioRef: "blob://mic/1"})
	}()
	return out
}

func wait(t *testing.T, errc <-chan error) {
	t.Helper()
	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("turn: %v", err)
		}
	// Longer than the engine's own stall detector, so a serialising
	// orchestrator reports the stalled step rather than a bare timeout.
	case <-time.After(3 * patience):
		t.Fatal("turn never finished")
	}
}

func toolNames(calls []journal.Call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.Tool
	}
	return out
}

func callByID(t *testing.T, st journal.State, id string) journal.Call {
	t.Helper()
	for _, c := range st.Calls {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no call %q in %+v", id, st.Calls)
	return journal.Call{}
}

func (g *gateTool) ran() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.done
}

func (r *rig) kinds(t *testing.T, convID string) []journal.Kind {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	out := make([]journal.Kind, 0, len(events))
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func countKind(kinds []journal.Kind, k journal.Kind) int {
	n := 0
	for _, got := range kinds {
		if got == k {
			n++
		}
	}
	return n
}

// eventOf returns the single event of a kind, failing if there is not exactly one.
func (r *rig) eventOf(t *testing.T, convID string, k journal.Kind) journal.Event {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	var found []journal.Event
	for _, e := range events {
		if e.Kind == k {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d %s events, want 1", len(found), k)
	}
	return found[0]
}

// awaitKind waits for an event the implementation is already committed to
// writing. Proves a later action was generated before a test cancels the turn:
// a cancelled model stream stops emitting, so cancelling too early makes the
// action never exist rather than be discarded.
func (r *rig) awaitKind(t *testing.T, convID string, k journal.Kind) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if countKind(r.kinds(t, convID), k) > 0 {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("no %s event was recorded", k)
}

// awaitCall waits for a detached child to land its result. The wait converges
// on a write that is already in flight; it is not a timing assumption.
func (r *rig) awaitCall(t *testing.T, convID, id string) journal.Call {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		st, err := journal.Replay(context.Background(), r.store, convID, journal.Overrides{})
		if err == nil {
			for _, c := range st.Calls {
				if c.ID == id && c.Outcome != "" {
					return c
				}
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("call %q never produced a result", id)
	return journal.Call{}
}
