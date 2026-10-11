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

// Bob mutes the office mid-conversation. The ring shows the mute over the
// listening session, since nothing he says can be heard, and listens again
// once he unmutes.
//
// verifies SPEC §3.3.1
func TestTheRingShowsTheMuteOverTheSession(t *testing.T) {
	lights := &ring{}
	panel := session.NewPanel(lights)
	r := newRigWith(t, nil, nil, nil, func(c *session.Config) { c.Ring = panel })

	s := r.open(t, "bob")
	lights.await(t, session.RingListening)
	panel.Mute(true)
	lights.await(t, session.RingMuted)
	panel.Mute(false)
	lights.await(t, session.RingListening)

	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	panel.Mute(true)
	want := []session.RingState{
		session.RingListening, session.RingMuted, session.RingListening, session.RingOff, session.RingMuted,
	}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v: the mute shown with or without a session", got, want)
	}
}

// The speech-to-text sidecar fails while the kitchen is idle. The ring
// shows the error, a session opened meanwhile shows what it is doing over
// it, and the error stays until the ear answers again.
//
// verifies SPEC §3.3.1, §7
func TestTheRingShowsAFaultWhileNoSessionIsOpen(t *testing.T) {
	lights := &ring{}
	panel := session.NewPanel(lights)
	r := newRigWith(t, nil, nil, nil, func(c *session.Config) { c.Ring = panel })

	panel.Fault(session.FaultEar)
	lights.await(t, session.RingError)

	s := r.open(t, "alice")
	lights.await(t, session.RingListening)
	if err := s.Close(t.Context(), "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	lights.await(t, session.RingError)

	panel.Recover(session.FaultEar)
	panel.Recover(session.FaultEar)
	want := []session.RingState{session.RingError, session.RingListening, session.RingError, session.RingOff}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v", got, want)
	}
}

// The mute outranks a fault: a muted kitchen is the household's choice,
// and the one thing the ring must never hide.
//
// verifies SPEC §3.3.1
func TestTheMuteOutranksAFault(t *testing.T) {
	lights := &ring{}
	panel := session.NewPanel(lights)

	panel.Fault(session.FaultJournal)
	panel.Mute(true)
	panel.Fault(session.FaultModel)
	panel.Mute(false)
	panel.Recover(session.FaultJournal)
	panel.Recover(session.FaultModel)

	want := []session.RingState{session.RingError, session.RingMuted, session.RingError, session.RingOff}
	if got := lights.states(); !slices.Equal(got, want) {
		t.Errorf("the ring showed %v, want %v", got, want)
	}
}
