package session_test

import (
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/session"
)

// ring is the kitchen's LED ring: every state it was shown, in order.
type ring struct {
	mu    sync.Mutex
	shown []session.RingState
}

func (r *ring) Show(s session.RingState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shown = append(r.shown, s)
}

func (r *ring) states() []session.RingState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]session.RingState(nil), r.shown...)
}

// await waits for the ring to be showing s.
func (r *ring) await(t *testing.T, s session.RingState) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		if got := r.states(); len(got) > 0 && got[len(got)-1] == s {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("the ring shows %v, want it to end on %s", r.states(), s)
}

// Alice asks the kitchen to find Led Zeppelin. The ring listens after the
// wake, speaks while "one sec" plays, works while the search is still out,
// listens again once the turn is done, and goes dark when the session ends.
//
// verifies SPEC §3.3.1
func TestTheRingShowsListeningWorkingAndSpeaking(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"led zeppelin"}`}},
		{act: session.SpeechDelta{CallID: "s1", Text: "One sec, searching.", Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	lights := &ring{}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Ring = lights })
	search := newGateTool(`{"hits":3}`)
	r.tools["media_search"] = search
	r.speaker.hold = true

	s := r.open(t, "alice")
	lights.await(t, session.RingListening)

	errc := heard(s, "play something by led zeppelin")
	search.enter(t)
	r.speaker.wrote(t)
	lights.await(t, session.RingSpeaking)

	close(r.speaker.release)
	lights.await(t, session.RingThinking)

	close(search.release)
	wait(t, errc)
	lights.await(t, session.RingListening)

	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	want := []session.RingState{
		session.RingListening, session.RingThinking, session.RingSpeaking,
		session.RingThinking, session.RingListening, session.RingOff,
	}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v", got, want)
	}
}

// Teagan cuts the kitchen off and walks away, and the session closes while
// the cut is still settling. The ring goes dark at the close, and nothing
// the closing session's children do after it lights it again.
//
// verifies SPEC §3.3.1
func TestTheRingGoesDarkWhenTheSessionCloses(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	lights := &ring{}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Ring = lights })
	r.speaker.hold = true

	s := r.open(t, "teagan")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)
	lights.await(t, session.RingSpeaking)

	if err := s.Close(t.Context(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}
	wait(t, errc)
	want := []session.RingState{session.RingListening, session.RingThinking, session.RingSpeaking, session.RingOff}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v: dark from the close on", got, want)
	}
}
