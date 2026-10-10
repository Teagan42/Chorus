package session_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// verifies SPEC §4.1
func TestToolDispatchesWhenItsJSONClosesNotAtEndOfMessage(t *testing.T) {
	dispatched := make(chan struct{})
	steps := []step{
		// The engine refuses to emit anything further until the call ran, so
		// an orchestrator that buffered to end of message stalls here.
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`}, gate: dispatched},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: `{"tool_calls":[{"id":"c1"}]}`}},
	}
	r := newRig(t, steps, map[string]session.Tool{
		"media_search": session.ToolFunc(func(context.Context, string) (string, error) {
			close(dispatched)
			return `{"hits":3}`, nil
		}),
	})

	s := r.open(t, "alice")
	wait(t, heard(s, "find zeppelin"))

	if stall := r.engine.stalled(); stall != "" {
		t.Fatalf("action stream stalled: %s", stall)
	}
	st := r.state(t, s.ConversationID())
	want := []journal.Call{{
		ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`,
		Outcome: "ok", Result: `{"hits":3}`,
	}}
	if !reflect.DeepEqual(st.Calls, want) {
		t.Errorf("calls = %+v, want %+v", st.Calls, want)
	}
}

// verifies SPEC §4.1
func TestSpeechAndToolCallAreConcurrentChildren(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`}},
		{act: session.SpeechDelta{CallID: "s1", Text: "one sec", Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	// The tool only succeeds if speech reached TTS while it was still running.
	spoke := make(chan struct{})
	r.tools["media_search"] = session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
		select {
		case <-spoke:
			return `{"hits":3}`, nil
		case <-time.After(patience):
			return "", errors.New("tool finished before anything was spoken")
		}
	})
	go func() {
		<-r.speaker.written
		close(spoke)
	}()

	s := r.open(t, "alice")
	wait(t, heard(s, "find zeppelin"))

	st := r.state(t, s.ConversationID())
	search := callByID(t, st, "c1")
	if search.Outcome != "ok" || search.Result != `{"hits":3}` {
		t.Errorf("media_search = %+v; speech did not overlap the call", search)
	}
	if !reflect.DeepEqual(st.Spoken, []string{"one sec"}) {
		t.Errorf("spoken = %q, want [one sec]", st.Spoken)
	}
}

// verifies SPEC §4.1
func TestActionsAreJournalledInTheOrderTheModelEmittedThem(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "a", Tool: "speak", Args: `{"text":"one sec"}`}},
		{act: session.ToolCall{ID: "b", Tool: "media_search", Args: `{"query":"zep"}`}},
		{act: session.ToolCall{ID: "c", Tool: "speak", Args: `{"text":"found three"}`}},
		{act: session.ToolCall{ID: "d", Tool: "remember", Args: `{"fact":"likes zeppelin"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	ok := session.ToolFunc(func(context.Context, string) (string, error) { return `{}`, nil })
	r := newRig(t, steps, map[string]session.Tool{"media_search": ok, "remember": ok})

	s := r.open(t, "alice")
	wait(t, heard(s, "find zeppelin and remember that"))

	st := r.state(t, s.ConversationID())
	// speak holds no privileged position: it interleaves exactly where the
	// model put it (ADR-0003).
	want := []string{"speak", "media_search", "speak", "remember"}
	if got := toolNames(st.Calls); !reflect.DeepEqual(got, want) {
		t.Errorf("call order = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(st.Spoken, []string{"one sec", "found three"}) {
		t.Errorf("spoken = %q, want [one sec found three]", st.Spoken)
	}
}

// verifies SPEC §4.1
func TestInlineContentIsRecordedAsAnImplicitSpeak(t *testing.T) {
	steps := []step{
		// Templates that emit content alongside tool_calls give speech no id.
		{act: session.SpeechDelta{Text: "turning them off", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: `{"content":"turning them off"}`}},
	}
	r := newRig(t, steps, nil)

	s := r.open(t, "alice")
	wait(t, heard(s, "lights off"))

	st := r.state(t, s.ConversationID())
	if got := toolNames(st.Calls); !reflect.DeepEqual(got, []string{"speak"}) {
		t.Errorf("calls = %q; inline content must be journalled as a speak call", got)
	}
	if !reflect.DeepEqual(st.Spoken, []string{"turning them off"}) {
		t.Errorf("spoken = %q, want [turning them off]", st.Spoken)
	}
}

// verifies SPEC §4.1
func TestSpeechDeltasReachTTSAsEmitted(t *testing.T) {
	played := make(chan struct{})
	steps := []step{
		// The second delta is withheld until the first one reached TTS, so an
		// orchestrator that waits for end of message stalls here.
		{act: session.SpeechDelta{CallID: "s1", Text: "I found "}, gate: played},
		{act: session.SpeechDelta{CallID: "s1", Text: "three "}},
		{act: session.SpeechDelta{CallID: "s1", Text: "albums", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	go func() {
		<-r.speaker.written
		close(played)
	}()

	s := r.open(t, "alice")
	wait(t, heard(s, "find zeppelin"))

	if stall := r.engine.stalled(); stall != "" {
		t.Fatalf("deltas did not stream: %s", stall)
	}
	st := r.state(t, s.ConversationID())
	// One utterance, assembled from both deltas.
	if !reflect.DeepEqual(st.Spoken, []string{"I found three albums"}) {
		t.Errorf("spoken = %q, want [I found three albums]", st.Spoken)
	}
}

// verifies SPEC §4
func TestLifecycleIsWhichChildrenAreAlive(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: "{}"}},
		{act: session.SpeechDelta{CallID: "s1", Text: "one sec", Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	gate := newGateTool(`{"hits":3}`)
	r.tools["media_search"] = gate
	r.speaker.hold = true

	s := r.open(t, "alice")
	if got := s.Children(); !reflect.DeepEqual(got, []string{"listening"}) {
		t.Errorf("children after wake = %q, want [listening]", got)
	}

	errc := heard(s, "find zeppelin")
	gate.enter(t)
	r.speaker.wrote(t)

	// Four concurrent children, no stage enum anywhere.
	if got := s.Children(); !reflect.DeepEqual(got, []string{"listening", "speaking", "thinking", "tool:c1"}) {
		t.Errorf("children mid-turn = %q", got)
	}

	close(gate.release)
	close(r.speaker.release)
	wait(t, errc)

	if got := s.Children(); !reflect.DeepEqual(got, []string{"listening"}) {
		t.Errorf("children after turn = %q, want [listening]", got)
	}
}
