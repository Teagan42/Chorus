package hass_test

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// These run the real supervisor over the real adapter, with only the wire and
// the speaker faked. internal/session's doubles are not importable, so the
// minimum of them is replicated here.

// stillClock never advances and never fires a timer: nothing in these tests is
// a question about time, and a timer that cannot fire cannot flake.
type stillClock struct{ now time.Time }

func (c stillClock) Now() time.Time                       { return c.now }
func (c stillClock) After(time.Duration) <-chan time.Time { return make(chan time.Time) }

// scriptEngine replays a fixed action stream to the utterance, in order, as
// fast as the session drains it. The follow-up ask the session makes with
// the tool's result is kept and answered with nothing.
type scriptEngine struct {
	acts []session.Action

	mu       sync.Mutex
	followUp []session.Input
}

func (e *scriptEngine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	acts := e.acts
	if n := len(in.Dialogue); n > 0 && in.Dialogue[n-1].Kind == journal.EntryResult {
		e.mu.Lock()
		e.followUp = append(e.followUp, in)
		e.mu.Unlock()
		acts = nil
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

// heldSpeaker reports each write and, when hold is set, keeps the utterance
// playing until released, so a test can see the speaking child alive beside
// the tool child.
type heldSpeaker struct {
	hold    bool
	cut     int
	written chan string
	release chan struct{}
}

func newSpeaker() *heldSpeaker {
	return &heldSpeaker{written: make(chan string, 8), release: make(chan struct{})}
}

func (sp *heldSpeaker) Open(ctx context.Context, callID string) (session.Stream, error) {
	return &heldStream{sp: sp, ctx: ctx, callID: callID}, nil
}

func (sp *heldSpeaker) wrote(t *testing.T) string {
	t.Helper()
	select {
	case s := <-sp.written:
		return s
	case <-time.After(patience):
		t.Fatal("the speaker was never written to")
		return ""
	}
}

type heldStream struct {
	sp     *heldSpeaker
	ctx    context.Context
	callID string
	text   string
}

func (s *heldStream) Write(text string) error {
	s.text += text
	s.sp.written <- text
	return nil
}

func (s *heldStream) Close() session.Playback {
	if s.sp.hold {
		select {
		case <-s.sp.release:
		case <-s.ctx.Done():
			cut := min(s.sp.cut, len(s.text))
			return session.Playback{
				Spoken: s.text[:cut], Unspoken: s.text[cut:],
				Frames: int64(cut) * 160, Truncated: true, AudioRef: "blob://tts/" + s.callID,
			}
		}
	}
	return session.Playback{Spoken: s.text, Frames: int64(len(s.text)) * 160, AudioRef: "blob://tts/" + s.callID}
}

type rig struct {
	sup     *session.Supervisor
	store   *journal.MemStore
	engine  *scriptEngine
	speaker *heldSpeaker
	wire    *transport
}

func newRig(t *testing.T, wire *transport, acts ...session.Action) *rig {
	t.Helper()
	eng := &scriptEngine{acts: acts}
	r := newRigOn(t, wire, eng)
	r.engine = eng
	return r
}

// newRigOn runs the supervisor over any engine, for a test whose model has to
// answer from what it is told.
func newRigOn(t *testing.T, wire *transport, eng session.Engine) *rig {
	t.Helper()
	store := journal.NewMemStore()
	clk := stillClock{now: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)}
	sp := newSpeaker()
	sup, err := session.New(session.Config{
		Journal: journal.New(store, clk, journal.Versions{Model: "qwen3-32b", Prompt: "p1", ToolSchema: "t1"}),
		Store:   store,
		Clock:   clk,
		Timers:  clk,
		Engine:  eng,
		Speaker: sp,
		Tools:   hass.Tools(clientOn(t, wire)),
		Gate:    session.Gate{MinEnergy: 0.2, MinWords: 2, Household: []string{"alice"}},
	})
	if err != nil {
		t.Fatalf("new supervisor: %v", err)
	}
	return &rig{sup: sup, store: store, speaker: sp, wire: wire}
}

func (r *rig) open(t *testing.T) *session.Session {
	t.Helper()
	s, err := r.sup.Open(context.Background(), session.Wake{Satellite: "kitchen", PersonID: "alice"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return s
}

func (r *rig) state(t *testing.T, convID string) journal.State {
	t.Helper()
	st, err := journal.Replay(context.Background(), r.store, convID, journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return st
}

// awaitCall converges on a result the implementation is already committed to
// writing; it is not a timing assumption.
func (r *rig) awaitCall(t *testing.T, convID, id string) journal.Call {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		for _, c := range r.state(t, convID).Calls {
			if c.ID == id && c.Outcome != "" {
				return c
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("call %q never produced a result", id)
	return journal.Call{}
}

func heard(s *session.Session, text string) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{Text: text, SpeakerID: "alice", AudioRef: "blob://mic/1"})
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
	case <-time.After(3 * patience):
		t.Fatal("turn never finished")
	}
}

const (
	ack      = "Turning off the kitchen lights."
	turnOff  = `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`
	nowOff   = `[{"entity_id":"light.kitchen","state":"off","attributes":{"friendly_name":"Kitchen"}}]`
	offAgain = `{"changed":[{"entity_id":"light.kitchen","state":"off"}]}`
)

// Speak-while-tooling with the one real tool: the acknowledgement is on the
// speaker while the service call is still on the wire, and both are live
// children of the same turn rather than stages of a pipeline.
//
// verifies SPEC §4.1, §14
func TestTheAcknowledgementPlaysWhileTheServiceCallIsInFlight(t *testing.T) {
	wire := newTransport(http.StatusOK, nowOff)
	wire.hold = true
	r := newRig(t, wire,
		session.SpeechDelta{CallID: "s1", Text: ack, Last: true},
		session.ToolCall{ID: "c1", Tool: "ha_call_service", Args: turnOff},
		session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"},
	)
	r.speaker.hold = true

	s := r.open(t)
	errc := heard(s, "turn off the kitchen lights")

	// Both in flight at once: the request has reached HA and the speaker is
	// playing, and neither waited for the other.
	wire.enter(t)
	if got := r.speaker.wrote(t); got != ack {
		t.Fatalf("spoken = %q, want %q", got, ack)
	}
	if got := s.Children(); !reflect.DeepEqual(got, []string{"listening", "speaking", "thinking", "tool:c1"}) {
		t.Errorf("children mid-turn = %q, want speaking and tool:c1 alive together", got)
	}
	if req := wire.last(t); req.method != http.MethodPost || req.path != "/api/services/light/turn_off" || req.auth != "Bearer "+token {
		t.Errorf("request on the wire = %+v", req)
	}

	close(wire.release)
	close(r.speaker.release)
	wait(t, errc)

	st := r.state(t, s.ConversationID())
	if !reflect.DeepEqual(st.Spoken, []string{ack}) {
		t.Errorf("spoken = %q, want [%q]", st.Spoken, ack)
	}
	var call journal.Call
	for _, c := range st.Calls {
		if c.ID == "c1" {
			call = c
		}
	}
	if call.Outcome != "ok" || call.Result != offAgain {
		t.Errorf("call = %+v, want ok with %s", call, offAgain)
	}
	// What HA said goes back to the model before the turn ends (ADR-0037).
	r.engine.mu.Lock()
	defer r.engine.mu.Unlock()
	if n := len(r.engine.followUp); n != 1 {
		t.Fatalf("model asked again %d times with the result, want 1", n)
	}
	d := r.engine.followUp[0].Dialogue
	if got := d[len(d)-1]; got.CallID != "c1" || got.Result != offAgain {
		t.Errorf("follow-up ended on %+v, want HA's answer", got)
	}
}

// A barge-in during the call: the request is already with HA, so the policy
// is detach, and the result it comes back with is kept for the model, marked
// as having landed after the cut (SPEC §4.4).
//
// verifies SPEC §4.4, §6
func TestABargeInDetachesTheServiceCallAndKeepsItsResult(t *testing.T) {
	wire := newTransport(http.StatusOK, nowOff)
	wire.hold = true
	r := newRig(t, wire,
		session.SpeechDelta{CallID: "s1", Text: ack, Last: true},
		session.ToolCall{ID: "c1", Tool: "ha_call_service", Args: turnOff},
		session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"},
	)
	r.speaker.hold = true
	r.speaker.cut = len("Turning off")

	s := r.open(t)
	errc := heard(s, "turn off the kitchen lights")
	wire.enter(t)
	r.speaker.wrote(t)

	ok, err := s.BargeIn(context.Background(), session.Candidate{
		PositionMS: 400, AudioRef: "blob://mic/2", SpeakerID: "alice", Energy: 0.9, Partial: "no wait leave them",
	})
	if err != nil || !ok {
		t.Fatalf("barge-in admitted=%v err=%v", ok, err)
	}
	// The turn ends on the barge-in; the call does not. HA answers afterwards.
	wait(t, errc)
	close(wire.release)

	call := r.awaitCall(t, s.ConversationID(), "c1")
	if call.Outcome != "detached" || call.Result != offAgain {
		t.Errorf("call = %+v, want detached with the changed state kept", call)
	}
	st := r.state(t, s.ConversationID())
	if !st.Interrupted || !reflect.DeepEqual(st.Spoken, []string{"Turning off"}) {
		t.Errorf("state = %+v, want the truncated acknowledgement recorded", st)
	}
}

// HA refusing the call is a result the model reasons about, not canned
// speech from the orchestrator (SPEC §7).
//
// verifies SPEC §7
func TestAnHAFailureComesBackAsAToolResult(t *testing.T) {
	wire := newTransport(http.StatusBadRequest, `{"message":"Service light.explode not found."}`)
	r := newRig(t, wire,
		session.ToolCall{ID: "c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"explode","entity_id":"light.kitchen"}`},
		session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"},
	)
	s := r.open(t)
	wait(t, heard(s, "make the lights explode"))

	st := r.state(t, s.ConversationID())
	call := st.Calls[len(st.Calls)-1]
	if call.Outcome != "error" || !strings.Contains(call.Result, `{"error":"call light.explode: 400 Bad Request`) {
		t.Errorf("call = %+v, want an error result naming the refusal", call)
	}
	if len(st.Spoken) != 0 {
		t.Errorf("spoken = %q, want nothing canned", st.Spoken)
	}
}
