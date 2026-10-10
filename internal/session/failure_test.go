package session_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// ------------------------------------------------------------------ doubles

// downEngine is Ollama refusing the connection: every ask fails before a
// single chunk, as engine.Turn reports a dial or a non-200 (SPEC §7).
type downEngine struct {
	err error

	mu    sync.Mutex
	asked int
}

func (e *downEngine) Turn(context.Context, session.Input) (<-chan session.Action, error) {
	e.mu.Lock()
	e.asked++
	e.mu.Unlock()
	return nil, e.err
}

func (e *downEngine) asks() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.asked
}

// hungEngine is a model that goes quiet: it emits what it was given, each
// action once its gate opens, then nothing at all until it is cancelled, when
// it ends the stream as the Ollama decoder does (internal/provider/ollama).
// Once answering is set, later asks answer it instead, as a model that has
// come back would.
type hungEngine struct {
	acts  []session.Action
	gates []chan struct{}

	mu        sync.Mutex
	answering []session.Action
	asked     int
}

func (e *hungEngine) Turn(ctx context.Context, _ session.Input) (<-chan session.Action, error) {
	e.mu.Lock()
	e.asked++
	answer := e.answering
	e.mu.Unlock()
	out := make(chan session.Action)
	go func() {
		defer close(out)
		if answer != nil {
			for _, a := range answer {
				out <- a
			}
			return
		}
		for i, a := range e.acts {
			if i < len(e.gates) && e.gates[i] != nil {
				select {
				case <-e.gates[i]:
				case <-ctx.Done():
					out <- session.TurnEnd{FinishReason: session.FinishError, Completion: `{"error":"context canceled"}`, Error: ctx.Err().Error()}
					return
				}
			}
			out <- a
			if _, ended := a.(session.TurnEnd); ended {
				return
			}
		}
		<-ctx.Done()
		out <- session.TurnEnd{FinishReason: session.FinishError, Completion: `{"error":"context canceled"}`, Error: ctx.Err().Error()}
	}()
	return out, nil
}

func (e *hungEngine) answer(acts ...session.Action) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.answering = acts
}

// kokoro is the voice as the satellite reports it: a call named in dies is
// heard for that many bytes before the synthesiser stops answering, and the
// stream says so rather than claiming a cut. stalls names calls the device
// never confirms the end of. refuse is a voice that will not open at all.
// Canned lines play: the satellite has them rendered (satellite.Canned).
type kokoro struct {
	dies   map[string]int
	stalls map[string]int
	refuse bool

	mu     sync.Mutex
	played []string
}

func (k *kokoro) Open(_ context.Context, callID string) (session.Stream, error) {
	if k.refuse {
		return nil, errors.New("satellite: open audio for " + callID + ": blob: disk full")
	}
	return &kokoroStream{k: k, callID: callID}, nil
}

func (k *kokoro) heard() []string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]string(nil), k.played...)
}

type kokoroStream struct {
	k      *kokoro
	callID string
	text   string
}

func (s *kokoroStream) Write(text string) error {
	s.text += text
	return nil
}

func (s *kokoroStream) Close() session.Playback {
	ref := "blob://tts/" + s.callID
	if n, ok := s.k.dies[s.callID]; ok {
		s.k.mu.Lock()
		s.k.played = append(s.k.played, s.text[:n])
		s.k.mu.Unlock()
		return session.Playback{
			Spoken: s.text[:n], Unspoken: s.text[n:], AudioRef: ref, Frames: int64(n) * 160, Truncated: true,
			Failure: fmt.Errorf("%w: synthesize %q: kokoro: 503 Service Unavailable", session.ErrVoiceUnavailable, s.text[n:]),
		}
	}
	if n, ok := s.k.stalls[s.callID]; ok {
		return session.Playback{
			Spoken: s.text[:n], Unspoken: s.text[n:], AudioRef: ref, Frames: int64(n) * 160, Truncated: true,
			Failure: session.ErrPlaybackUnconfirmed,
		}
	}
	s.k.mu.Lock()
	s.k.played = append(s.k.played, s.text)
	s.k.mu.Unlock()
	return session.Playback{Spoken: s.text, AudioRef: ref, Frames: int64(len(s.text)) * 160}
}

// ------------------------------------------------------------------ helpers

// awaitDeadline spins until a timer of d is armed: the model's deadline is
// armed by its own goroutine, and the backstop re-arms on every utterance, so
// a count of timers cannot tell them apart.
func (c *clock) awaitDeadline(t *testing.T, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for time.Now().Before(deadline) {
		for _, w := range c.waits() {
			if w == d {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("no %v timer armed; armed: %v", d, c.waits())
}

func (r *rig) all(t *testing.T, convID string) []journal.Event {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return events
}

func ofKind(events []journal.Event, k journal.Kind) []journal.Event {
	var out []journal.Event
	for _, e := range events {
		if e.Kind == k {
			out = append(out, e)
		}
	}
	return out
}

// noBargeIn fails on anything in the log that says the person interrupted:
// a failure recorded as a barge-in is a preference pair nobody gave.
func noBargeIn(t *testing.T, events []journal.Event) {
	t.Helper()
	for _, e := range events {
		if e.Kind == journal.KindBargeInDetected || e.Fields["reason"] == "barge_in" {
			t.Errorf("seq %d %s %v reads as the person interrupting", e.Seq, e.Kind, e.Fields)
		}
	}
}

// cannedCall is the speak call the log says a failure apologised with.
func cannedCall(t *testing.T, events []journal.Event, id string) journal.Event {
	t.Helper()
	for _, e := range ofKind(events, journal.KindToolCalled) {
		if e.Fields["call_id"] == id {
			if !strings.Contains(e.Fields["args_json"], `"canned":true`) {
				t.Errorf("canned call %s args = %s, want it marked canned", id, e.Fields["args_json"])
			}
			return e
		}
	}
	t.Fatalf("no speak call %q for the canned line", id)
	return journal.Event{}
}

func spokenTexts(events []journal.Event) []string {
	var out []string
	for _, e := range ofKind(events, journal.KindSpeechSpoken) {
		out = append(out, e.Fields["text"])
	}
	return out
}

func failRig(t *testing.T, eng session.Engine, voice session.Speaker, tweak func(*session.Config)) *rig {
	t.Helper()
	return newRigWith(t, nil, nil, nil, func(c *session.Config) {
		c.Engine = eng
		if voice != nil {
			c.Speaker = voice
		}
		if tweak != nil {
			tweak(c)
		}
	})
}

// ------------------------------------------------------------------ the model

// Teagan asks the kitchen to turn the lights off while Ollama is down. The
// house says it cannot think rather than going silent, the failure is in
// the log, and the turn ends cleanly: there is nothing to ask again.
//
// verifies SPEC §7
func TestOllamaDownSaysSoInsteadOfGoingSilent(t *testing.T) {
	eng := &downEngine{err: errors.New("ollama chat: Post \"http://10.0.0.20:11434/api/chat\": dial tcp 10.0.0.20:11434: connect: connection refused")}
	voice := &kokoro{}
	r := failRig(t, eng, voice, nil)

	s := r.open(t, "teagan")
	if err := s.Heard(context.Background(), session.Transcript{Text: "turn off the kitchen lights", SpeakerID: "teagan", AudioRef: "blob://mic/1"}); err != nil {
		t.Fatalf("Heard = %v, want the failure said and journalled, not returned", err)
	}

	events := r.all(t, s.ConversationID())
	failed := r.eventOf(t, s.ConversationID(), journal.KindModelFailed)
	if failed.Fields["reason"] != "unavailable" || !strings.Contains(failed.Fields["error"], "connection refused") {
		t.Errorf("model_failed = %v, want unavailable with the engine's error", failed.Fields)
	}
	if failed.Versions.Model != "qwen3-32b" {
		t.Errorf("model_failed versions = %+v, want the model that failed", failed.Versions)
	}
	id := failed.Fields["canned_call_id"]
	cannedCall(t, events, id)
	if got := spokenTexts(events); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("spoken = %q, want the canned line %q", got, session.DefaultCanned.Model)
	}
	if got := voice.heard(); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("the kitchen played %q, want the canned line", got)
	}
	if eng.asks() != 1 {
		t.Errorf("the model was asked %d times, want once: no model, no follow-up", eng.asks())
	}

	// The model that comes back is told the person was answered, and is not
	// shown the apology as its own words (ADR-0051).
	st := r.state(t, s.ConversationID())
	var said []journal.Entry
	for _, e := range st.Dialogue {
		if e.Kind == journal.EntrySaid {
			said = append(said, e)
		}
	}
	if len(said) != 1 || !said[0].Canned || said[0].Text != session.DefaultCanned.Model {
		t.Errorf("said = %+v, want the canned line marked canned", said)
	}
	if got := s.Children(); len(got) != 1 || got[0] != "listening" {
		t.Errorf("children = %v, want the session listening for the next try", got)
	}
}

// The model starts answering and its stream breaks: what was heard stays
// heard, and the apology follows it.
//
// verifies SPEC §7
func TestAStreamThatBreaksMidAnswerApologisesAfterWhatWasSaid(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Let me check the garage door.", Last: true}},
		{act: session.ToolCall{ID: "call_g1", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`}},
		{act: session.TurnEnd{FinishReason: session.FinishError, Completion: `{"error":"unexpected EOF"}`, Error: "ollama: unexpected EOF"}},
	}
	r := newRig(t, steps, map[string]session.Tool{
		"ha_get_state": session.ToolFunc(func(context.Context, string) (string, error) {
			return `{"state":"open"}`, nil
		}),
	})

	s := r.open(t, "alice")
	wait(t, heard(s, "is the garage door shut"))

	events := r.all(t, s.ConversationID())
	failed := r.eventOf(t, s.ConversationID(), journal.KindModelFailed)
	if failed.Fields["reason"] != "failed" || failed.Fields["error"] != "ollama: unexpected EOF" {
		t.Errorf("model_failed = %v, want failed: ollama: unexpected EOF", failed.Fields)
	}
	want := []string{"Let me check the garage door.", session.DefaultCanned.Model}
	if got := spokenTexts(events); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("spoken = %q, want %q", got, want)
	}
	// The call it made still ran; asking again with a broken model would not
	// be worth the wait.
	if c := callByID(t, r.state(t, s.ConversationID()), "call_g1"); c.Outcome != "ok" {
		t.Errorf("garage door read = %+v, want it to have run", c)
	}
	if n := len(r.engine.asks()); n != 1 {
		t.Errorf("the model was asked %d times, want once", n)
	}
}

// A model that never answers used to hold the session open forever, the 20 s
// backstop skipping while it thought, and the satellite stayed deaf to its
// own household. Now the ask is given up on, the house says so, and the next
// thing said is heard.
//
// verifies SPEC §4.5, §7
func TestAHungModelIsGivenUpOnAndTheKitchenHearsAgain(t *testing.T) {
	eng := &hungEngine{}
	r := failRig(t, eng, nil, func(c *session.Config) {
		// Shorter than the backstop, so advancing to it fires nothing else.
		c.ModelTimeout = 15 * time.Second
	})

	s := r.open(t, "alan")
	errc := heard(s, "what's on the calendar today")
	r.clock.awaitDeadline(t, 15*time.Second)
	r.clock.advance(15 * time.Second)
	wait(t, errc)

	failed := r.eventOf(t, s.ConversationID(), journal.KindModelFailed)
	if failed.Fields["reason"] != "timed_out" || !strings.Contains(failed.Fields["error"], "15s") {
		t.Errorf("model_failed = %v, want timed_out after 15s", failed.Fields)
	}
	if got := spokenTexts(r.all(t, s.ConversationID())); len(got) != 1 || got[0] != session.DefaultCanned.Model {
		t.Errorf("spoken = %q, want the canned line", got)
	}
	select {
	case <-s.Done():
		t.Fatal("the session closed; it should be listening for the next try")
	default:
	}

	// Ollama finishes loading. Alan asks again on the same session.
	eng.answer(
		session.SpeechDelta{CallID: "call_s2", Text: "You have a dentist appointment at three.", Last: true},
		session.TurnEnd{FinishReason: "stop", Completion: "{}"},
	)
	wait(t, heard(s, "what's on the calendar today"))
	got := spokenTexts(r.all(t, s.ConversationID()))
	if len(got) != 2 || got[1] != "You have a dentist appointment at three." {
		t.Errorf("spoken = %q, want the answer after the apology", got)
	}
}

// The deadline is how long the model has said nothing, not how long it has
// taken: a model that keeps acting is working, however long the turn.
//
// verifies SPEC §7
func TestTheModelsDeadlineRestartsWithEveryAction(t *testing.T) {
	read := make(chan struct{})
	done := make(chan struct{})
	eng := &hungEngine{
		acts: []session.Action{
			session.SpeechDelta{CallID: "call_s1", Text: "Checking the thermostat.", Last: true},
			session.ToolCall{ID: "call_t1", Tool: "ha_get_state", Args: `{"entity_id":"climate.hallway"}`},
			session.TurnEnd{FinishReason: "stop", Completion: "{}"},
		},
		gates: []chan struct{}{nil, read, done},
	}
	r := failRig(t, eng, nil, func(c *session.Config) {
		c.ModelTimeout = 15 * time.Second
		c.Tools = map[string]session.Tool{"ha_get_state": session.ToolFunc(func(context.Context, string) (string, error) {
			return `{"state":"heat","current_temperature":19.5}`, nil
		})}
	})

	s := r.open(t, "teagan")
	errc := heard(s, "how warm is the hallway")
	r.awaitKind(t, s.ConversationID(), journal.KindSpeechSpoken)
	r.clock.advance(10 * time.Second)
	close(read)
	r.awaitCall(t, s.ConversationID(), "call_t1")
	// 20 s into the ask, 10 s since the model last acted.
	r.clock.advance(10 * time.Second)
	// The read earns a follow-up ask, which has nothing to add.
	eng.answer(session.TurnEnd{FinishReason: "stop", Completion: "{}"})
	close(done)
	wait(t, errc)

	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindModelFailed); n != 0 {
		t.Errorf("%d model_failed for a model that kept acting", n)
	}
}

// The person walking away while the model thinks is not the model failing:
// the session's end is the reason, and nothing apologises for it.
//
// verifies SPEC §7
func TestTheSessionEndingWhileTheModelThinksIsNoModelFailure(t *testing.T) {
	r := failRig(t, &hungEngine{}, nil, nil)

	s := r.open(t, "alice")
	errc := heard(s, "remind me to call the vet")
	r.clock.awaitDeadline(t, session.DefaultModelTimeout)
	if err := s.Close(context.Background(), "device_lost"); err != nil {
		t.Fatalf("close: %v", err)
	}
	wait(t, errc)

	kinds := r.kinds(t, s.ConversationID())
	if n := countKind(kinds, journal.KindModelFailed); n != 0 {
		t.Errorf("%d model_failed for a session that ended", n)
	}
	if n := countKind(kinds, journal.KindSpeechSpoken); n != 0 {
		t.Errorf("%d utterances spoken into a session that ended", n)
	}
}

// ------------------------------------------------------------------ the voice

// Kokoro stops answering halfway through the forecast. The kitchen heard
// "Tomorrow will be sunny," and nothing more; that is recorded as the voice
// failing, never as Alice interrupting, so no preference pair is cut from
// it. The rest of the turn's speech is dropped for the same reason, and the
// apology rendered at startup plays in its place.
//
// verifies SPEC §7, §9.1
func TestKokoroDyingMidAnswerIsAVoiceFailureNotABargeIn(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "Tomorrow will be sunny, with a high of nineteen.", Last: true}},
		{act: session.SpeechDelta{CallID: "call_s2", Text: "Want me to set an umbrella reminder anyway?", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	voice := &kokoro{dies: map[string]int{"call_s1": len("Tomorrow will be sunny,")}}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Speaker = voice })

	s := r.open(t, "alice")
	wait(t, heard(s, "what's the weather tomorrow"))

	events := r.all(t, s.ConversationID())
	noBargeIn(t, events)

	failed := ofKind(events, journal.KindSpeechFailed)
	if len(failed) != 1 || failed[0].Fields["call_id"] != "call_s1" || failed[0].Fields["reason"] != "tts_unavailable" ||
		!strings.Contains(failed[0].Fields["error"], "503") {
		t.Fatalf("speech_failed = %v, want one for call_s1 with Kokoro's error", failed)
	}
	cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated)
	if cut.Fields["reason"] != "tts_unavailable" || cut.Fields["spoken_text"] != "Tomorrow will be sunny," {
		t.Errorf("speech_truncated = %v, want the heard half, cut by the voice", cut.Fields)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_s1"); c.Outcome != "error" || c.Result != `{"error":"tts_unavailable"}` {
		t.Errorf("call_s1 = %+v, want an error result naming the voice", c)
	}
	for _, d := range ofKind(events, journal.KindSpeechDiscarded) {
		if d.Fields["reason"] != "tts_unavailable" {
			t.Errorf("speech_discarded %v, want the voice named as the reason", d.Fields)
		}
	}

	id := failed[0].Fields["canned_call_id"]
	cannedCall(t, events, id)
	want := []string{"Tomorrow will be sunny,", session.DefaultCanned.Voice}
	if got := voice.heard(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("the kitchen played %q, want %q", got, want)
	}

	// Told on the next ask: the voice failed, the person did not cut in.
	st := r.state(t, s.ConversationID())
	for _, e := range st.Dialogue {
		if e.CallID == "call_s1" && (!e.Cut || e.CutBy != "tts_unavailable") {
			t.Errorf("call_s1 told as %+v, want cut by the voice", e)
		}
	}
}

// Nothing will open, not even the apology: each failure is recorded, the
// apology is tried once, and the turn ends rather than looping.
//
// verifies SPEC §7
func TestAVoiceThatWillNotOpenIsTriedForOneApologyOnly(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "The front door is locked.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Speaker = &kokoro{refuse: true} })

	s := r.open(t, "bob")
	if err := s.Heard(context.Background(), session.Transcript{Text: "is the front door locked", SpeakerID: "bob", AudioRef: "blob://mic/1"}); err != nil {
		t.Fatalf("Heard = %v, want the failure journalled, not returned", err)
	}

	events := r.all(t, s.ConversationID())
	noBargeIn(t, events)
	failed := ofKind(events, journal.KindSpeechFailed)
	if len(failed) != 2 {
		t.Fatalf("%d speech_failed, want the answer's and the apology's", len(failed))
	}
	id := failed[0].Fields["canned_call_id"]
	if failed[0].Fields["call_id"] != "call_s1" || id == "" {
		t.Errorf("first speech_failed = %v, want call_s1 apologised for", failed[0].Fields)
	}
	if failed[1].Fields["call_id"] != id || failed[1].Fields["canned_call_id"] != "" {
		t.Errorf("second speech_failed = %v, want the apology's own, with no apology for it", failed[1].Fields)
	}
	var unheard []string
	for _, d := range ofKind(events, journal.KindSpeechDiscarded) {
		unheard = append(unheard, d.Fields["unspoken_text"]+"/"+d.Fields["reason"])
	}
	want := []string{"The front door is locked./tts_unavailable", session.DefaultCanned.Voice + "/tts_unavailable"}
	if strings.Join(unheard, "|") != strings.Join(want, "|") {
		t.Errorf("discarded = %q, want %q", unheard, want)
	}
}

// Ollama streams the forecast as inline content, a clause at a time, and
// the voice will not open for the first clause. The call's result is the
// voice failing, and the clause that arrives after is no second, cancelling
// result over it. Every discard names its call, so the apology that could
// not be said either is never taken for the model's words.
//
// verifies SPEC §7, §9.1
func TestAStreamedAnswerKeepsTheVoicesFailureAsItsResult(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_w1", Text: "Tomorrow will be sunny, "}},
		{act: session.SpeechDelta{CallID: "call_w1", Text: "with a high of nineteen.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Speaker = &kokoro{refuse: true} })

	s := r.open(t, "alice")
	wait(t, heard(s, "what's the weather tomorrow"))

	events := r.all(t, s.ConversationID())
	noBargeIn(t, events)
	var results []string
	for _, e := range ofKind(events, journal.KindToolResult) {
		if e.Fields["call_id"] == "call_w1" {
			results = append(results, e.Fields["outcome"])
		}
	}
	if strings.Join(results, ",") != "error" {
		t.Errorf("call_w1 results = %q, want the one error", results)
	}
	id := ofKind(events, journal.KindSpeechFailed)[0].Fields["canned_call_id"]
	var discarded []string
	for _, d := range ofKind(events, journal.KindSpeechDiscarded) {
		discarded = append(discarded, d.Fields["call_id"]+"/"+d.Fields["reason"]+"/"+d.Fields["unspoken_text"])
	}
	want := []string{
		"call_w1/tts_unavailable/Tomorrow will be sunny, ",
		"call_w1/tts_unavailable/with a high of nineteen.",
		id + "/tts_unavailable/" + session.DefaultCanned.Voice,
	}
	if !slices.Equal(sorted(discarded), sorted(want)) {
		t.Errorf("discarded = %q, want %q", discarded, want)
	}
}

func sorted(s []string) []string { return slices.Sorted(slices.Values(s)) }

// One apology per turn, whichever failed first: a model that is down and a
// voice that cannot say so do not stack apologies.
//
// verifies SPEC §7
func TestTheModelAndTheVoiceBothDownApologiseOnce(t *testing.T) {
	r := failRig(t, &downEngine{err: errors.New("ollama chat: 503 Service Unavailable: model is loading")}, &kokoro{refuse: true}, nil)

	s := r.open(t, "teagan")
	wait(t, heard(s, "set a timer for ten minutes"))

	events := r.all(t, s.ConversationID())
	model := r.eventOf(t, s.ConversationID(), journal.KindModelFailed)
	voice := r.eventOf(t, s.ConversationID(), journal.KindSpeechFailed)
	if voice.Fields["call_id"] != model.Fields["canned_call_id"] || voice.Fields["canned_call_id"] != "" {
		t.Errorf("speech_failed = %v after model_failed %v, want the apology failing with no second apology", voice.Fields, model.Fields)
	}
	if n := len(ofKind(events, journal.KindToolCalled)); n != 1 {
		t.Errorf("%d speak calls, want the one apology", n)
	}
}

// The device stops reporting playback partway: what it confirmed is heard,
// the rest is not, and neither the person nor the voice is blamed. The
// voice still works, so the turn's next utterance plays.
//
// verifies SPEC §3.2.1, §7
func TestADeviceThatStopsConfirmingPlaybackIsNotABargeIn(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "call_s1", Text: "The dishwasher finished at nine.", Last: true}},
		{act: session.SpeechDelta{CallID: "call_s2", Text: "It's ready to unload.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	voice := &kokoro{stalls: map[string]int{"call_s1": len("The dishwasher finished")}}
	r := newRigWith(t, steps, nil, nil, func(c *session.Config) { c.Speaker = voice })

	s := r.open(t, "alan")
	wait(t, heard(s, "is the dishwasher done"))

	events := r.all(t, s.ConversationID())
	noBargeIn(t, events)
	failed := r.eventOf(t, s.ConversationID(), journal.KindSpeechFailed)
	if failed.Fields["reason"] != "playback_unconfirmed" || failed.Fields["canned_call_id"] != "" {
		t.Errorf("speech_failed = %v, want playback_unconfirmed with no apology", failed.Fields)
	}
	if cut := r.eventOf(t, s.ConversationID(), journal.KindSpeechTruncated); cut.Fields["reason"] != "playback_unconfirmed" {
		t.Errorf("speech_truncated reason = %q, want playback_unconfirmed", cut.Fields["reason"])
	}
	if got := spokenTexts(events); len(got) != 1 || got[0] != "It's ready to unload." {
		t.Errorf("spoken = %q, want the next utterance still played", got)
	}
}
