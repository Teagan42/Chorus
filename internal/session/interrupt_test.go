package session_test

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

const line = "I found three albums by that artist"

// interruption is the candidate that passes every stage of the gate.
func interruption(pos int) session.Candidate {
	return session.Candidate{
		PositionMS: pos, AudioRef: "blob://mic/2",
		SpeakerID: "alice", Energy: 0.9, Partial: "no the other one",
	}
}

// verifies SPEC §4.4
func TestBargeInKeepsWhatWasSpokenWithItsTruncationPoint(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)

	if ok, err := s.BargeIn(t.Context(), interruption(420)); err != nil || !ok {
		t.Fatalf("barge-in: ok=%v err=%v", ok, err)
	}
	wait(t, errc)

	cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	// The split is exact because it comes from DAC frames, not an estimate
	// (SPEC §3.2.1), and it names the call it cut so the next ask is told
	// where it said it, and says the person cut it (ADR-0051).
	want := map[string]string{
		"spoken_text":   "I found three",
		"unspoken_text": " albums by that artist",
		"frames_played": "2080",
		"call_id":       "s1",
		"reason":        "barge_in",
	}
	if !maps.Equal(cut.Fields, want) {
		t.Errorf("truncation = %v, want %v", cut.Fields, want)
	}
	if cut.AudioRef == "" {
		t.Error("truncation carries no audio reference")
	}

	st := r.state(t, s.ConversationID())
	if !st.Interrupted {
		t.Error("state is not marked interrupted")
	}
	if !reflect.DeepEqual(st.Spoken, []string{"I found three"}) {
		t.Errorf("spoken = %q; what was heard must be kept", st.Spoken)
	}
	if !reflect.DeepEqual(st.Unspoken, []string{" albums by that artist"}) {
		t.Errorf("unspoken = %q; the remainder must be recorded, not dropped", st.Unspoken)
	}
	// The conversation survives the interruption; only the children died.
	if !st.Open {
		t.Error("barge-in closed the session")
	}
	if got := s.Children(); !reflect.DeepEqual(got, []string{"listening"}) {
		t.Errorf("children after barge-in = %q, want [listening]", got)
	}
}

// verifies SPEC §4.4
func TestBargeInKeepsToolResultsThatAlreadyArrived(t *testing.T) {
	landed := make(chan struct{})
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`}, gate: landed},
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, map[string]session.Tool{
		"media_search": session.ToolFunc(func(context.Context, string) (string, error) {
			return `{"hits":3}`, nil
		}),
	})
	// Gated on the record, not the tool's return: the outcome is stamped after
	// the tool returns, and a gate opened inside the tool races that stamp.
	r.store.recorded(journal.KindToolResult, "c1", landed)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)
	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	wait(t, errc)

	// The model must see "I said this much, and this tool already answered".
	if got := callByID(t, r.state(t, s.ConversationID()), "c1"); got.Outcome != "ok" || got.Result != `{"hits":3}` {
		t.Errorf("call = %+v; an arrived result must survive the interruption", got)
	}
}

// verifies SPEC §4.4
func TestCancelPolicyAbandonsTheCallOnBargeIn(t *testing.T) {
	steps := []step{
		// remember declares on_interrupt: cancel (the registry default).
		{act: session.ToolCall{ID: "c1", Tool: "remember", Args: `{"fact":"likes zeppelin"}`}},
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	gate := newGateTool(`{"stored":true}`)
	r.tools["remember"] = gate
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "remember that")
	gate.enter(t)
	r.speaker.wrote(t)

	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	wait(t, errc)

	if got := r.awaitCall(t, s.ConversationID(), "c1").Outcome; got != "cancelled" {
		t.Errorf("outcome = %q, want cancelled", got)
	}
	if gate.ran() {
		t.Error("a cancel-policy tool completed after the barge-in")
	}
}

// verifies SPEC §4.4
func TestDetachPolicyFinishesAndKeepsTheResult(t *testing.T) {
	steps := []step{
		// media_search declares on_interrupt: detach (ADR-0011).
		{act: session.ToolCall{ID: "c1", Tool: "media_search", Args: `{"query":"zep"}`}},
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	gate := newGateTool(`{"hits":3}`)
	r.tools["media_search"] = gate
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	gate.enter(t)
	r.speaker.wrote(t)

	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	// A detached child no longer holds the turn open.
	wait(t, errc)

	close(gate.release)
	got := r.awaitCall(t, s.ConversationID(), "c1")
	if got.Outcome != "detached" || got.Result != `{"hits":3}` {
		t.Errorf("call = %+v, want outcome detached with the result kept", got)
	}
	if !gate.ran() {
		t.Error("a detach-policy tool was cancelled instead of finishing")
	}
}

// verifies SPEC §4.4
func TestUninterruptibleCallMustComplete(t *testing.T) {
	specs := maps.Clone(registry.Specs)
	// No shipped tool declares this yet; the policy still has to be honoured.
	specs["unlock_door"] = registry.ToolSpec{
		Name: "unlock_door", OnInterrupt: registry.InterruptUninterruptible,
		Scope: registry.ScopeHousehold, Timeout: registry.Specs["remember"].Timeout,
		RequiresConfirmation: true,
	}
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "unlock_door", Args: `{"confirmation":"cf_4c1e9a07"}`}},
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	gate := newGateTool(`{"unlocked":true}`)
	r := newRigSpecs(t, steps, map[string]session.Tool{"unlock_door": gate}, specs)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	// Alice was already asked; this utterance is her yes.
	r.held(t, s.ConversationID(), "c0", "unlock_door", `{}`, "cf_4c1e9a07")
	errc := heard(s, "yes, unlock the door")
	gate.enter(t)
	r.speaker.wrote(t)

	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	// Side effects are already committed, so the barge-in must not stop it and
	// the turn must not be reported finished while it runs.
	select {
	case <-errc:
		t.Fatal("turn finished while an uninterruptible call was in flight")
	default:
	}

	close(gate.release)
	wait(t, errc)

	if got := r.awaitCall(t, s.ConversationID(), "c1"); got.Outcome != "ok" {
		t.Errorf("outcome = %q, want ok", got.Outcome)
	}
	if !gate.ran() {
		t.Error("an uninterruptible tool was cancelled")
	}
}

// verifies SPEC §4.4
func TestQueuedSpeechIsDiscardedAsADistinctEvent(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.SpeechDelta{CallID: "s2", Text: "and it came out in 1973", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)
	// The engine's sends are unbuffered, so a recorded model_completed proves
	// s2 was queued. s1 is held, so the turn is parked with s2 still waiting -
	// exactly the state a barge-in has to discard.
	r.awaitKind(t, s.ConversationID(), journal.KindModelCompleted)
	if _, err := s.BargeIn(t.Context(), interruption(420)); err != nil {
		t.Fatalf("barge-in: %v", err)
	}
	wait(t, errc)

	kinds := r.kinds(t, s.ConversationID())
	if n := countKind(kinds, journal.KindSpeechTruncated); n != 1 {
		t.Errorf("%d truncation events, want 1 (the utterance that was playing)", n)
	}
	if n := countKind(kinds, journal.KindSpeechDiscarded); n != 1 {
		t.Errorf("%d discard events, want 1 (the utterance still queued)", n)
	}
	if n := countKind(kinds, journal.KindSpeechSpoken); n != 0 {
		t.Errorf("%d spoken events, want 0: nothing played to completion", n)
	}

	st := r.state(t, s.ConversationID())
	// Only the heard half may reach the model; the rest is recorded, not shown.
	if !reflect.DeepEqual(st.Spoken, []string{"I found three"}) {
		t.Errorf("spoken = %q, want [I found three]", st.Spoken)
	}
	// Order is not asserted: the cut utterance and the dropped queue are
	// recorded by different children, so which lands first is a race. Replay
	// stays deterministic because it reads the recorded order.
	want := []string{" albums by that artist", "and it came out in 1973"}
	got := slices.Clone(st.Unspoken)
	slices.Sort(got)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unspoken = %q, want %q", got, want)
	}
}

// verifies SPEC §4.3
func TestRejectedBargeInIsJournalledAndSpeechContinues(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRig(t, steps, nil)
	r.speaker.hold = true

	s := r.open(t, "alice")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)

	cases := []rejection{
		{"television", session.Candidate{Energy: 0.05, SpeakerID: "alice", Partial: "two words"}, "vad"},
		{"wrong housemate", session.Candidate{Energy: 0.9, SpeakerID: "stranger", Partial: "two words"}, "speaker_id"},
		// Identification is on and did not recognise the voice: that is the
		// television, not a missing sidecar (ADR-0031).
		{"unidentified voice", session.Candidate{Energy: 0.9, SpeakerID: "", Partial: "two words"}, "speaker_id"},
		{"a cough", session.Candidate{Energy: 0.9, SpeakerID: "alice", Partial: "uh"}, "partial_length"},
	}
	for _, c := range cases {
		c.cand.AudioRef = "blob://mic/rejected"
		ok, err := s.BargeIn(t.Context(), c.cand)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ok {
			t.Errorf("%s passed the gate", c.name)
		}
	}

	close(r.speaker.release)
	wait(t, errc)

	if got := r.stages(t, s.ConversationID()); !reflect.DeepEqual(got, stagesOf(cases)) {
		t.Errorf("rejected stages = %q, want %q: they are the tuning corpus", got, stagesOf(cases))
	}
	// A rejected candidate changes nothing: speech ran to completion.
	if st := r.state(t, s.ConversationID()); !reflect.DeepEqual(st.Spoken, []string{line}) || st.Interrupted {
		t.Errorf("spoken = %q interrupted = %v; a rejection must not stop speech", st.Spoken, st.Interrupted)
	}
}

// A household that runs without speaker identification has no speaker to
// match, so the gate cannot demand one: energy and partial length still
// gate, and a voice nobody can name interrupts (ADR-0031).
//
// verifies SPEC §4.3
func TestBargeInWithoutSpeakerIdentificationGatesOnEnergyAndWords(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, func(cfg *session.Config) { cfg.Gate.SpeakerIDUnavailable = true })
	r.speaker.hold = true
	r.speaker.cut = len("I found three")

	s := r.open(t, "")
	errc := heard(s, "find zeppelin")
	r.speaker.wrote(t)

	rejected := []rejection{
		{"quiet", session.Candidate{Energy: 0.05, Partial: "two words", AudioRef: "blob://mic/quiet"}, "vad"},
		{"a cough", session.Candidate{Energy: 0.9, Partial: "uh", AudioRef: "blob://mic/cough"}, "partial_length"},
	}
	for _, c := range rejected {
		ok, err := s.BargeIn(t.Context(), c.cand)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ok {
			t.Errorf("%s passed the gate with identification off", c.name)
		}
	}

	unnamed := interruption(420)
	unnamed.SpeakerID = ""
	if ok, err := s.BargeIn(t.Context(), unnamed); err != nil || !ok {
		t.Fatalf("barge-in by an unidentified voice: ok=%v err=%v, want it admitted", ok, err)
	}
	wait(t, errc)

	if got := r.stages(t, s.ConversationID()); !reflect.DeepEqual(got, stagesOf(rejected)) {
		t.Errorf("rejected stages = %q, want %q: the other stages still gate", got, stagesOf(rejected))
	}
	cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	if cut.Fields["spoken_text"] != "I found three" {
		t.Errorf("truncation = %v; the interruption must have stopped speech", cut.Fields)
	}
	if st := r.state(t, s.ConversationID()); !st.Interrupted {
		t.Error("state is not marked interrupted")
	}
}

// A detection the speaker stage never judged says so, so the tuning corpus
// can tell it from one a household voice passed (ADR-0031).
//
// verifies SPEC §4.3
func TestADetectionRecordsWhetherTheSpeakerStageRan(t *testing.T) {
	for _, c := range []struct {
		name    string
		skipped bool
		want    string
	}{
		{"identification on", false, ""},
		{"identification off", true, "true"},
	} {
		t.Run(c.name, func(t *testing.T) {
			steps := []step{
				{act: session.SpeechDelta{CallID: "s1", Text: line, Last: true}},
				{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
			}
			r := newRigWith(t, steps, nil, nil, func(cfg *session.Config) { cfg.Gate.SpeakerIDUnavailable = c.skipped })
			r.speaker.hold = true

			s := r.open(t, "alice")
			errc := heard(s, "find zeppelin")
			r.speaker.wrote(t)
			if ok, err := s.BargeIn(t.Context(), interruption(420)); err != nil || !ok {
				t.Fatalf("barge-in: ok=%v err=%v", ok, err)
			}
			wait(t, errc)

			detected := r.eventOf(t, s.ConversationID(), journal.KindBargeInDetected)
			if got := detected.Fields["speaker_stage_skipped"]; got != c.want {
				t.Errorf("speaker_stage_skipped = %q, want %q", got, c.want)
			}
		})
	}
}

// rejection is a candidate the gate must refuse, and the stage that does.
type rejection struct {
	name  string
	cand  session.Candidate
	stage string
}

func stagesOf(cases []rejection) []string {
	out := make([]string, len(cases))
	for i, c := range cases {
		out[i] = c.stage
	}
	return out
}

// A tool's timeout tells the model now, but it is not a barge-in: work the
// registry says outlives an interruption outlives its timeout too, and what
// it finally returns is kept, as nobody waits for it any more.
//
// verifies SPEC §4.4
func TestATimedOutDetachCallFinishesAndKeepsTheResult(t *testing.T) {
	steps := []step{
		// timer_start declares on_interrupt: detach.
		{act: session.ToolCall{ID: "c1", Tool: "timer_start", Args: `{"seconds":720,"label":"oven"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	gate := newGateTool(`{"timer_id":"t_3f9c2a10"}`)
	r := newRig(t, steps, map[string]session.Tool{"timer_start": gate})

	s := r.open(t, "teagan")
	errc := heard(s, "set an oven timer for twelve minutes")
	gate.enter(t)
	timeout := registry.Specs["timer_start"].Timeout
	r.clock.awaitDeadline(t, timeout)
	r.clock.advance(timeout)
	// The model is told it timed out, and the turn goes on without it.
	wait(t, errc)
	if got := callByID(t, r.state(t, s.ConversationID()), "c1"); got.Outcome != "timed_out" {
		t.Fatalf("outcome = %q, want timed_out once the timeout passed", got.Outcome)
	}

	close(gate.release)
	got := r.awaitOutcome(t, s.ConversationID(), "c1", "detached")
	if got.Result != `{"timer_id":"t_3f9c2a10"}` {
		t.Errorf("call = %+v, want the late result kept", got)
	}
	if !gate.ran() {
		t.Error("a detach-policy tool was cancelled by its timeout")
	}
}

// verifies SPEC §4.4
func TestATimedOutUninterruptibleCallIsNotCancelled(t *testing.T) {
	specs := maps.Clone(registry.Specs)
	specs["unlock_door"] = registry.ToolSpec{
		Name: "unlock_door", OnInterrupt: registry.InterruptUninterruptible,
		Scope: registry.ScopeHousehold, Timeout: registry.Specs["remember"].Timeout,
		RequiresConfirmation: true,
	}
	steps := []step{
		{act: session.ToolCall{ID: "c1", Tool: "unlock_door", Args: `{"confirmation":"cf_4c1e9a07"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	gate := newGateTool(`{"unlocked":true}`)
	r := newRigSpecs(t, steps, map[string]session.Tool{"unlock_door": gate}, specs)

	s := r.open(t, "alice")
	r.held(t, s.ConversationID(), "c0", "unlock_door", `{}`, "cf_4c1e9a07")
	errc := heard(s, "yes, unlock the door")
	gate.enter(t)
	r.clock.awaitDeadline(t, specs["unlock_door"].Timeout)
	r.clock.advance(specs["unlock_door"].Timeout)
	wait(t, errc)

	// Side effects are already committed: the timeout's return must not stop it.
	close(gate.release)
	if got := r.awaitOutcome(t, s.ConversationID(), "c1", "detached"); got.Result != `{"unlocked":true}` {
		t.Errorf("call = %+v, want the late result kept", got)
	}
	if !gate.ran() {
		t.Error("an uninterruptible tool was cancelled by its timeout")
	}
}

// verifies SPEC §7
func TestATimedOutCancelCallIsCancelled(t *testing.T) {
	steps := []step{
		// ha_get_state declares on_interrupt: cancel.
		{act: session.ToolCall{ID: "c1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	gate := newGateTool(`{"state":"closed"}`)
	r := newRig(t, steps, map[string]session.Tool{"ha_get_state": gate})

	s := r.open(t, "alan")
	errc := heard(s, "is the garage door closed")
	gate.enter(t)
	timeout := registry.Specs["ha_get_state"].Timeout
	r.clock.awaitDeadline(t, timeout)
	r.clock.advance(timeout)
	wait(t, errc)

	if got := callByID(t, r.state(t, s.ConversationID()), "c1"); got.Outcome != "timed_out" || got.Result != `{"error":"timed_out"}` {
		t.Errorf("call = %+v, want timed_out", got)
	}
	if gate.ran() {
		t.Error("a cancel-policy tool ran on past its timeout")
	}
}
