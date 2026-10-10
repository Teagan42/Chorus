package session_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// verifies SPEC §4.5
func TestWakeWordOpensTheSession(t *testing.T) {
	r := newRig(t, nil, nil)

	s, err := r.sup.Open(t.Context(), session.Wake{
		Satellite: "kitchen", PersonID: "alice", Confidence: 0.91,
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	st := r.state(t, s.ConversationID())
	if !st.Open || st.Satellite != "kitchen" || st.Speaker != "alice" {
		t.Errorf("state = %+v, want an open session for alice on kitchen", st)
	}
	if got := r.eventOf(t, s.ConversationID(), journal.KindSessionOpened).Fields["wake_confidence"]; got != "0.91" {
		t.Errorf("wake_confidence = %q, want 0.91", got)
	}
}

// verifies SPEC §4.5
func TestModelClosesTheSessionWithEndSession(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "e1", Tool: "end_session", Args: "{}"}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	s := r.open(t, "alice")
	wait(t, heard(s, "thanks, that's all"))

	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("end_session did not close the session")
	}

	st := r.state(t, s.ConversationID())
	if st.Open || st.CloseReason != "model_ended" {
		t.Errorf("state = %+v, want closed with reason model_ended", st)
	}
	// The model decided; no timer was involved.
	if got := callByID(t, st, "e1").Outcome; got != "ok" {
		t.Errorf("end_session outcome = %q, want ok", got)
	}
}

// The model is told to speak before it ends the session, so a farewell and
// end_session arrive in the same turn. Close interrupts the speech channel, so
// closing on the call itself cuts the goodbye off mid-word.
//
// verifies SPEC §4.5
func TestEndSessionLetsTheFarewellFinish(t *testing.T) {
	const farewell = "Goodnight!"
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: farewell, Last: true}},
		{act: session.ToolCall{ID: "e1", Tool: "end_session", Args: "{}"}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	// Playback still in flight when end_session is dispatched, which is the
	// ordinary case: speech is a child and the stream does not wait for it.
	r.speaker.hold = true
	r.speaker.cut = len("Good")

	s := r.open(t, "alice")
	errc := heard(s, "that's all, goodnight")
	r.speaker.wrote(t)

	// The closer is already running by the time end_session's result is
	// recorded, so releasing playback after this is not a timing assumption.
	r.awaitCall(t, s.ConversationID(), "e1")
	close(r.speaker.release)
	wait(t, errc)

	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("end_session never closed the session")
	}

	st := r.state(t, s.ConversationID())
	if !slices.Equal(st.Spoken, []string{farewell}) {
		t.Errorf("spoken = %q, want the whole farewell %q", st.Spoken, farewell)
	}
	if len(st.Unspoken) != 0 {
		t.Errorf("the close cut the farewell off: unspoken = %q", st.Unspoken)
	}
	// Ending a conversation is not an interruption of it.
	if st.Interrupted {
		t.Error("closing on the model's own request was recorded as a barge-in")
	}
	if st.Open || st.CloseReason != "model_ended" {
		t.Errorf("state = %+v, want closed with reason model_ended", st)
	}
}

// verifies SPEC §4.5
func TestSilenceBackstopClosesAnAbandonedSession(t *testing.T) {
	r := newRig(t, nil, nil)
	s := r.open(t, "alice")

	r.clock.awaitTimers(t, 1)
	if got := r.clock.waits(); !reflect.DeepEqual(got, []time.Duration{session.DefaultSilence}) {
		t.Errorf("armed timers = %v, want [%v]", got, session.DefaultSilence)
	}

	r.clock.advance(session.DefaultSilence)
	select {
	case <-s.Done():
	case <-time.After(patience):
		t.Fatal("the silence backstop never fired")
	}

	if st := r.state(t, s.ConversationID()); st.Open || st.CloseReason != "silence_timeout" {
		t.Errorf("state = %+v, want closed with reason silence_timeout", st)
	}
}

// verifies SPEC §4.5
func TestBackstopDoesNotCloseASessionWithLiveChildren(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)

	// Still talking: the backstop is a floor under silence, not a turn limit.
	r.clock.awaitTimers(t, 1)
	r.clock.advance(session.DefaultSilence)
	r.clock.awaitTimers(t, 1)

	select {
	case <-s.Done():
		t.Fatal("the backstop closed a session that was still speaking")
	default:
	}

	close(r.speaker.release)
	wait(t, errc)
	if st := r.state(t, s.ConversationID()); !st.Open {
		t.Errorf("state = %+v, want still open", st)
	}
}

// verifies SPEC §4.5
func TestMigratingDeviceResumesTheSameConversation(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "ok", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)

	kitchen, err := r.sup.Open(t.Context(), session.Wake{Satellite: "kitchen", PersonID: "alice"})
	if err != nil {
		t.Fatalf("open kitchen: %v", err)
	}
	wait(t, heard(kitchen, "turn off the lights"))
	if err := kitchen.Close(t.Context(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}

	// She walked to another room inside the migration window.
	r.clock.advance(90 * time.Second)
	office, err := r.sup.Open(t.Context(), session.Wake{Satellite: "office", PersonID: "alice"})
	if err != nil {
		t.Fatalf("open office: %v", err)
	}
	if !office.Resumed() || office.ConversationID() != kitchen.ConversationID() {
		t.Fatalf("office conversation = %q resumed=%v, want %q resumed=true",
			office.ConversationID(), office.Resumed(), kitchen.ConversationID())
	}
	wait(t, heard(office, "and the kettle too"))

	// One log, both devices: the conversation is the person's, the audio
	// stream is the device's (ADR-0006).
	st := r.state(t, office.ConversationID())
	if !reflect.DeepEqual(st.Heard, []string{"turn off the lights", "and the kettle too"}) {
		t.Errorf("heard = %q; context did not survive the migration", st.Heard)
	}
	if st.Satellite != "office" {
		t.Errorf("satellite = %q, want office", st.Satellite)
	}
	if !st.Open {
		t.Error("resumed conversation is not open")
	}
}

// verifies SPEC §4.5
func TestMigrationWindowExpiryStartsAFreshConversation(t *testing.T) {
	r := newRig(t, nil, nil)

	first := r.open(t, "alice")
	if err := first.Close(t.Context(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}
	r.clock.advance(session.MigrationWindow + time.Second)

	second := r.open(t, "alice")
	if second.Resumed() || second.ConversationID() == first.ConversationID() {
		t.Errorf("stale wake resumed %q", second.ConversationID())
	}
}

// verifies SPEC §4.2
func TestPreemptDropsWhatWasAboutToBeSaid(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "I'll check the", Last: true}},
		// A tool result invalidated the line that was playing.
		{act: session.SpeechDelta{CallID: "s2", Text: "it is already off", Mode: session.ModePreempt, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I'll check")

	s := r.open(t, "alice")
	errc := heard(s, "is the kettle on")

	// Only the preempting utterance is ever released: the first one can only
	// end by being cut, so a release cannot mask a missing preemption.
	r.speaker.wrote(t)
	if got := r.speaker.wrote(t); got != "it is already off" {
		t.Fatalf("second utterance = %q; the preempting line never played", got)
	}
	close(r.speaker.release)
	wait(t, errc)

	st := r.state(t, s.ConversationID())
	if !reflect.DeepEqual(st.Spoken, []string{"I'll check", "it is already off"}) {
		t.Errorf("spoken = %q, want the cut line then the preempting one", st.Spoken)
	}
	cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	if cut.Fields["unspoken_text"] != " the" {
		t.Errorf("unspoken = %q, want \" the\"", cut.Fields["unspoken_text"])
	}
}

// verifies SPEC §4.2
func TestATrailingDeltaForAPreemptedCallIsNeverSpoken(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "I'll check the"}},
		{act: session.SpeechDelta{CallID: "s2", Text: "it is already off", Mode: session.ModePreempt, Last: true}},
		// Generation for s1 had not stopped when the preempt landed.
		{act: session.SpeechDelta{CallID: "s1", Text: " kettle", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I'll check")

	s := r.open(t, "alice")
	errc := heard(s, "is the kettle on")

	r.speaker.wrote(t)
	if got := r.speaker.wrote(t); got != "it is already off" {
		t.Fatalf("second utterance = %q; the preempting line never played", got)
	}
	close(r.speaker.release)
	wait(t, errc)

	st := r.state(t, s.ConversationID())
	if !reflect.DeepEqual(st.Spoken, []string{"I'll check", "it is already off"}) {
		t.Errorf("spoken = %q; a preempted call spoke again", st.Spoken)
	}
	if !slices.Contains(st.Unspoken, " kettle") {
		t.Errorf("unspoken = %q, want the trailing delta recorded as never heard", st.Unspoken)
	}
}

// verifies SPEC §7
func TestToolFailuresComeBackAsResults(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: "{}"}},
		{act: session.ToolCall{ID: "c2", Tool: "no_such_tool", Args: "{}"}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{
		"media_search": session.ToolFunc(func(context.Context, string) (string, error) {
			return "", context.DeadlineExceeded
		}),
	})

	s := r.open(t, "alice")
	wait(t, heard(s, "find zeppelin"))

	st := r.state(t, s.ConversationID())
	// The orchestrator never speaks for itself here; the model reasons about
	// the failure instead (ADR-0008).
	if got := callByID(t, st, "c1").Outcome; got != "error" {
		t.Errorf("failed call outcome = %q, want error", got)
	}
	if got := callByID(t, st, "c2").Result; got != `{"error":"unknown_tool"}` {
		t.Errorf("undeclared tool result = %q", got)
	}
	if len(st.Spoken) != 0 {
		t.Errorf("spoken = %q, want nothing canned", st.Spoken)
	}
}

// The embedding rides on the transcript whether or not it matched anyone, so
// the corpus can cluster voices nobody enrolled. Absent, not empty: a
// resolver that was down leaves no field rather than an empty array.
//
// verifies SPEC §5
func TestHeardRecordsTheSpeakerEmbedding(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r := newRig(t, steps, nil)
	s := r.open(t, "alice")

	err := s.Heard(t.Context(), session.Transcript{
		Text: "hello", AudioRef: "blob://mic/1", Embedding: []float32{0.5, -0.25, 1},
	})
	if err != nil {
		t.Fatalf("heard: %v", err)
	}
	first := r.eventOf(t, s.ConversationID(), journal.KindUtteranceTranscribed)
	if got := first.Fields["embedding_json"]; got != "[0.5,-0.25,1]" {
		t.Errorf("embedding_json = %q, want [0.5,-0.25,1]", got)
	}

	wait(t, heard(s, "and again"))
	events, err := r.store.Events(t.Context(), s.ConversationID())
	if err != nil {
		t.Fatal(err)
	}
	var second journal.Event
	for _, e := range events {
		if e.Kind == journal.KindUtteranceTranscribed && e.Fields["text"] == "and again" {
			second = e
		}
	}
	if _, present := second.Fields["embedding_json"]; present {
		t.Errorf("a transcript with no embedding recorded %q", second.Fields["embedding_json"])
	}
}
