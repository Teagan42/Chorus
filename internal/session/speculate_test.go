package session_test

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

const lightsOff = `{"domain":"light","service":"turn_off","entity_id":"light.kitchen"}`

// handEngine is a model the test speaks for, one action at a time. A send
// returns once the session has taken the action, so a test knows exactly
// what was generated before it looks at the log.
type handEngine struct {
	mu   sync.Mutex
	asks []*handAsk
	next chan *handAsk
}

type handAsk struct {
	in  session.Input
	ctx context.Context
	out chan session.Action
}

func newHandEngine() *handEngine { return &handEngine{next: make(chan *handAsk, 8)} }

func (e *handEngine) Turn(ctx context.Context, in session.Input) (<-chan session.Action, error) {
	a := &handAsk{in: in, ctx: ctx, out: make(chan session.Action)}
	e.mu.Lock()
	e.asks = append(e.asks, a)
	e.mu.Unlock()
	e.next <- a
	return a.out, nil
}

// asked waits for the next ask.
func (e *handEngine) asked(t *testing.T) *handAsk {
	t.Helper()
	select {
	case a := <-e.next:
		return a
	case <-time.After(patience):
		t.Fatal("the model was never asked")
		return nil
	}
}

func (e *handEngine) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.asks)
}

// say sends actions and closes the stream, as a model finishing does.
func (a *handAsk) say(t *testing.T, acts ...session.Action) {
	t.Helper()
	a.send(t, acts...)
	close(a.out)
}

func (a *handAsk) send(t *testing.T, acts ...session.Action) {
	t.Helper()
	for _, act := range acts {
		select {
		case a.out <- act:
		case <-time.After(patience):
			t.Fatalf("nobody took %T", act)
		}
	}
}

// cut waits for the session to cancel the ask, then reports the end a
// cancelled model still owes.
func (a *handAsk) cut(t *testing.T, completion string) {
	t.Helper()
	select {
	case <-a.ctx.Done():
	case <-time.After(patience):
		t.Fatal("the speculative ask was never cancelled")
	}
	a.say(t, session.TurnEnd{FinishReason: session.FinishError, Completion: completion, Error: "context canceled"})
}

// lights is the kitchen's lights through Home Assistant: it counts the calls
// that actually ran.
type lights struct {
	mu   sync.Mutex
	runs []string
	// hold, when set, keeps the call from answering until it closes.
	hold <-chan struct{}
}

func (l *lights) Invoke(ctx context.Context, args string) (string, error) {
	l.mu.Lock()
	l.runs = append(l.runs, args)
	hold := l.hold
	l.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return `["light.kitchen"]`, nil
}

// answersAfterTheModel holds the call's result until the completion is in
// the log, so two runs record them in one order whatever the scheduler does.
func (l *lights) answersAfterTheModel(r *rig) {
	gate := make(chan struct{})
	r.store.recorded(journal.KindModelCompleted, "", gate)
	l.mu.Lock()
	l.hold = gate
	l.mu.Unlock()
}

func (l *lights) ran() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.runs...)
}

// speculationRig is a supervisor over a hand-driven model, with Teagan one
// turn into a kitchen conversation: she asked whether the lights are on and
// heard that they are.
func speculationRig(t *testing.T, tweak func(*session.Config)) (*rig, *handEngine, *lights, *session.Session) {
	t.Helper()
	eng, lit := newHandEngine(), &lights{}
	r := newRigWith(t, nil, map[string]session.Tool{"ha_call_service": lit}, nil, func(c *session.Config) {
		c.Engine = eng
		if tweak != nil {
			tweak(c)
		}
	})
	s := r.open(t, "teagan")
	turn := heardBy(s, "are the kitchen lights on", nil)
	eng.asked(t).say(t,
		session.ToolCall{ID: "s1", Tool: "speak", Args: `{"text":"They are."}`},
		session.TurnEnd{FinishReason: "stop", Completion: `{"say":"They are."}`},
	)
	wait(t, turn)
	return r, eng, lit, s
}

// heardBy runs Teagan's turn with the speculation made in its pause.
func heardBy(s *session.Session, text string, sp *session.Speculation) <-chan error {
	out := make(chan error, 1)
	go func() {
		out <- s.Heard(context.Background(), session.Transcript{
			Text: text, SpeakerID: "teagan", SpeakerMatch: "identified", AudioRef: "blob://mic/" + text, Speculation: sp,
		})
	}()
	return out
}

func guess(t *testing.T, s *session.Session, text string) *session.Speculation {
	t.Helper()
	sp := s.Speculate(context.Background(), session.Transcript{
		Text: text, SpeakerID: "teagan", SpeakerMatch: "identified", AudioRef: "blob://mic/" + text,
	})
	if sp == nil {
		t.Fatalf("no speculative ask for %q", text)
	}
	return sp
}

// turnThemOff is what the model answers "turn them off" with: the call and
// what it says over it.
func turnThemOff() []session.Action {
	return []session.Action{
		session.ToolCall{ID: "c1", Tool: "ha_call_service", Args: lightsOff},
		session.ToolCall{ID: "s2", Tool: "speak", Args: `{"text":"Kitchen lights off."}`},
		session.TurnEnd{FinishReason: "tool_calls", Completion: `{"tool_calls":["ha_call_service","speak"]}`},
	}
}

func eventsOf(t *testing.T, r *rig, convID string) []journal.Event {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	return events
}

// The pause after "turn them off" asks the model, which answers in full
// before the endpointer has ended the turn. Nothing runs and nothing is
// said until the turn is confirmed; then the answer is the turn's, and the
// log reads as if the model had been asked late.
//
// verifies SPEC §4.1, §8, §11
func TestATurnAskedInItsPauseIsKeptWhenNothingChanged(t *testing.T) {
	r, eng, lit, s := speculationRig(t, nil)
	sp := guess(t, s, "turn them off")
	ask := eng.asked(t)
	if ask.in.Text != "turn them off" || ask.in.Speaker != "teagan" || ask.in.Room != "" {
		t.Errorf("speculative ask = %q for %q, want turn them off for teagan", ask.in.Text, ask.in.Speaker)
	}
	if n := len(ask.in.Dialogue); n == 0 || ask.in.Dialogue[n-1].Text != "turn them off" {
		t.Errorf("speculative dialogue ends %+v, want the supposed utterance", ask.in.Dialogue)
	}
	ask.send(t, turnThemOff()...)
	before := len(eventsOf(t, r, s.ConversationID()))
	if got := lit.ran(); len(got) != 0 {
		t.Fatalf("a speculative call ran: %v", got)
	}
	if after := len(eventsOf(t, r, s.ConversationID())); after != before {
		t.Fatalf("speculation wrote %d events before the turn was confirmed", after-before)
	}

	// The endpointer holds the quiet another 600 ms before it ends the turn.
	r.clock.advance(600 * time.Millisecond)
	turn := heardBy(s, "turn them off", sp)
	close(ask.out)
	eng.asked(t).say(t, done)
	wait(t, turn)

	// The first turn, the pause's ask, and the follow-up with the result.
	if eng.count() != 3 {
		t.Errorf("model asked %d times, want 3: the turn was asked again", eng.count())
	}
	if got := lit.ran(); len(got) != 1 || got[0] != lightsOff {
		t.Errorf("lights ran %v, want the kept call once", got)
	}
	var kinds []journal.Kind
	var early string
	for _, e := range eventsOf(t, r, s.ConversationID()) {
		kinds = append(kinds, e.Kind)
		if e.Kind == journal.KindModelCompleted && e.Fields["asked_early_ms"] != "" {
			early = e.Fields["asked_early_ms"]
		}
		if e.Speculative {
			t.Errorf("a kept speculation left %s marked speculative", e.Kind)
		}
	}
	if early != "600" {
		t.Errorf("asked_early_ms = %q, want 600", early)
	}
	if countKind(kinds, journal.KindSpeculationDiscarded) != 0 {
		t.Error("a kept speculation was recorded as discarded")
	}
	st := r.state(t, s.ConversationID())
	if want := []string{"are the kitchen lights on", "turn them off"}; !reflect.DeepEqual(st.Heard, want) {
		t.Errorf("heard = %v, want %v", st.Heard, want)
	}
	if c := callByID(t, st, "c1"); c.Outcome != "ok" {
		t.Errorf("ha_call_service outcome = %q, want ok", c.Outcome)
	}
}

// The same conversation asked late and asked in its pause leaves the same
// log, but for when the model was asked: replay cannot tell them apart.
//
// verifies SPEC §8, §11, §15
func TestAKeptSpeculationReplaysAsALateAsk(t *testing.T) {
	late, lateEng, lateLights, lateSess := speculationRig(t, nil)
	lateLights.answersAfterTheModel(late)
	turn := heardBy(lateSess, "turn them off", nil)
	lateEng.asked(t).say(t, turnThemOff()...)
	lateEng.asked(t).say(t, done)
	wait(t, turn)

	early, earlyEng, earlyLights, earlySess := speculationRig(t, nil)
	earlyLights.answersAfterTheModel(early)
	sp := guess(t, earlySess, "turn them off")
	earlyEng.asked(t).say(t, turnThemOff()...)
	turn = heardBy(earlySess, "turn them off", sp)
	earlyEng.asked(t).say(t, done)
	wait(t, turn)

	lateEvents, earlyEvents := eventsOf(t, late, lateSess.ConversationID()), eventsOf(t, early, earlySess.ConversationID())
	for _, e := range earlyEvents {
		delete(e.Fields, "asked_early_ms")
	}
	// What the turn decided is in the order the model chose it; what its
	// children did settles as they finish, in either run.
	if a, b := decided(lateEvents), decided(earlyEvents); !reflect.DeepEqual(a, b) {
		t.Errorf("decisions differ\nlate  %v\nearly %v", a, b)
	}
	if a, b := everything(lateEvents), everything(earlyEvents); !reflect.DeepEqual(a, b) {
		t.Errorf("logs differ\nlate  %v\nearly %v", a, b)
	}
	lateState, earlyState := late.state(t, lateSess.ConversationID()), early.state(t, earlySess.ConversationID())
	if !reflect.DeepEqual(lateState.Dialogue, earlyState.Dialogue) || !reflect.DeepEqual(lateState.Calls, earlyState.Calls) {
		t.Errorf("replay differs\nlate  %+v\nearly %+v", lateState.Dialogue, earlyState.Dialogue)
	}
}

// decided is the log's events the turn wrote in its own order: what was
// heard and told, each call as the model made it, and each completion.
func decided(events []journal.Event) []string {
	var out []string
	for _, e := range events {
		switch e.Kind {
		case journal.KindUtteranceTranscribed, journal.KindMemoryRecalled, journal.KindToolCalled, journal.KindModelCompleted:
			out = append(out, fmt.Sprintf("%s %v", e.Kind, e.Fields))
		}
	}
	return out
}

// everything is every event in the log, in no order.
func everything(events []journal.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, fmt.Sprintf("%s %s %v", e.Kind, e.AudioRef, e.Fields))
	}
	slices.Sort(out)
	return out
}

// Teagan pauses after "turn them off" and goes on: "and put the hallway
// ones on". The guess is thrown away, cut off mid-answer; the log keeps one
// speculative record of it, nothing it generated ran, and the whole
// sentence is asked afresh.
//
// verifies SPEC §4.4, §8, §11
func TestASpeculationThePersonTalksThroughIsDiscarded(t *testing.T) {
	r, eng, lit, s := speculationRig(t, nil)
	sp := guess(t, s, "turn them off")
	ask := eng.asked(t)
	ask.send(t, session.ToolCall{ID: "c1", Tool: "ha_call_service", Args: lightsOff})
	before := r.state(t, s.ConversationID())

	r.clock.advance(340 * time.Millisecond)
	discarded := make(chan struct{})
	go func() {
		sp.Discard(session.DiscardResumed)
		close(discarded)
	}()
	ask.cut(t, `{"tool_calls":["ha_call_service"]}`)
	<-discarded

	if got := lit.ran(); len(got) != 0 {
		t.Fatalf("a discarded call ran: %v", got)
	}
	d := r.eventOf(t, s.ConversationID(), journal.KindSpeculationDiscarded)
	if !d.Speculative {
		t.Error("speculation_discarded is not marked speculative")
	}
	want := map[string]string{
		"text": "turn them off", "reason": "resumed", "speaker_id": "teagan",
		"completion_json": `{"tool_calls":["ha_call_service"]}`, "finish_reason": "error", "ran_ms": "340",
	}
	if !reflect.DeepEqual(d.Fields, want) {
		t.Errorf("speculation_discarded = %v, want %v", d.Fields, want)
	}
	after := r.state(t, s.ConversationID())
	after.LastSeq, after.Speculative = before.LastSeq, before.Speculative
	if !reflect.DeepEqual(after, before) {
		t.Errorf("a discarded speculation changed the conversation\nbefore %+v\nafter  %+v", before, after)
	}

	turn := heardBy(s, "turn them off and put the hallway ones on", nil)
	fresh := eng.asked(t)
	if fresh.in.Text != "turn them off and put the hallway ones on" {
		t.Errorf("fresh ask = %q, want the whole sentence", fresh.in.Text)
	}
	fresh.say(t, session.TurnEnd{FinishReason: "stop", Completion: `{}`})
	wait(t, turn)
	// Discarding again, as a late listener might, records nothing more.
	sp.Discard(session.DiscardNotATurn)
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindSpeculationDiscarded); n != 1 {
		t.Errorf("%d speculation_discarded, want 1", n)
	}
}

// A guess is only the turn's if the turn would have asked the same. Each
// case changes one thing between the pause and the confirmed turn; each
// throws the guess away and asks afresh with what the log now says.
//
// verifies SPEC §5, §8, §11
func TestASpeculationTheConfirmedTurnWouldNotHaveAskedIsDiscarded(t *testing.T) {
	cases := map[string]func(t *testing.T, r *rig, s *session.Session) session.Transcript{
		// The final decode heard more than the pause did.
		"other words": func(*testing.T, *rig, *session.Session) session.Transcript {
			return session.Transcript{Text: "turn them all off", SpeakerID: "teagan", SpeakerMatch: "identified", AudioRef: "blob://mic/2"}
		},
		// Alice answered from across the kitchen, and the voice is hers.
		"another speaker": func(*testing.T, *rig, *session.Session) session.Transcript {
			return session.Transcript{Text: "turn them off", SpeakerID: "alice", SpeakerMatch: "identified", AudioRef: "blob://mic/2"}
		},
		// 11:59 became 12:00 while the endpointer held the pause.
		"the minute turned over": func(_ *testing.T, r *rig, _ *session.Session) session.Transcript {
			r.clock.advance(time.Minute)
			return session.Transcript{Text: "turn them off", SpeakerID: "teagan", SpeakerMatch: "identified", AudioRef: "blob://mic/2"}
		},
		// A search the model started earlier landed between the pause and
		// the turn.
		"the log moved": func(t *testing.T, r *rig, s *session.Session) session.Transcript {
			j := journal.New(r.store, r.clock, versions())
			for _, rec := range []journal.Record{
				{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "media_search", "call_id": "c0", "args_json": `{"query":"Led Zeppelin"}`}},
				{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c0", "outcome": "detached", "result_json": `{"hits":3}`}},
			} {
				if _, err := j.Append(context.Background(), s.ConversationID(), rec); err != nil {
					t.Fatalf("append %s: %v", rec.Kind, err)
				}
			}
			return session.Transcript{Text: "turn them off", SpeakerID: "teagan", SpeakerMatch: "identified", AudioRef: "blob://mic/2"}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			r, eng, lit, s := speculationRig(t, func(c *session.Config) { c.Silence = time.Hour })
			sp := guess(t, s, "turn them off")
			ask := eng.asked(t)
			ask.send(t, turnThemOff()[0])

			tr := change(t, r, s)
			tr.Speculation = sp
			turn := make(chan error, 1)
			go func() { turn <- s.Heard(context.Background(), tr) }()
			ask.cut(t, `{"tool_calls":["ha_call_service"]}`)
			fresh := eng.asked(t)
			if fresh.in.Text != tr.Text {
				t.Errorf("fresh ask = %q, want %q", fresh.in.Text, tr.Text)
			}
			fresh.say(t, session.TurnEnd{FinishReason: "stop", Completion: `{}`})
			wait(t, turn)

			if got := lit.ran(); len(got) != 0 {
				t.Errorf("the discarded guess's call ran: %v", got)
			}
			d := r.eventOf(t, s.ConversationID(), journal.KindSpeculationDiscarded)
			if d.Fields["reason"] != session.DiscardChanged {
				t.Errorf("reason = %q, want changed", d.Fields["reason"])
			}
			for _, e := range eventsOf(t, r, s.ConversationID()) {
				if e.Kind == journal.KindModelCompleted && e.Fields["asked_early_ms"] != "" {
					t.Errorf("model_completed #%d claims an early ask", e.Seq)
				}
			}
		})
	}
}

// The session ends with a guess outstanding: it is discarded before the
// close, so the log never holds speculative work after its conversation
// ended without saying why.
//
// verifies SPEC §4.5, §11
func TestASpeculationOutstandingAtCloseIsDiscardedFirst(t *testing.T) {
	r, eng, _, s := speculationRig(t, nil)
	guess(t, s, "turn them off")
	ask := eng.asked(t)
	closed := make(chan error, 1)
	go func() { closed <- s.Close(context.Background(), "device_lost") }()
	ask.cut(t, "")
	if err := <-closed; err != nil {
		t.Fatalf("close: %v", err)
	}
	kinds := r.kinds(t, s.ConversationID())
	if n := len(kinds); n < 2 || kinds[n-2] != journal.KindSpeculationDiscarded || kinds[n-1] != journal.KindSessionClosed {
		t.Fatalf("log ends %v, want speculation_discarded then session_closed", kinds)
	}
	d := r.eventOf(t, s.ConversationID(), journal.KindSpeculationDiscarded)
	if d.Fields["reason"] != session.DiscardSessionClosed || d.Fields["completion_json"] != "" {
		t.Errorf("discard = %v, want session_closed with no completion", d.Fields)
	}
	if s.Speculate(context.Background(), session.Transcript{Text: "goodnight", SpeakerID: "teagan"}) != nil {
		t.Error("a closed session speculated")
	}
}

// A pause is not guessed at while the session is still answering, while a
// guess is already out, or when the words are a hot phrase the session
// answers itself.
//
// verifies SPEC §4.3, §11
func TestASessionDeclinesToSpeculateWhenItCannotKeepTheGuess(t *testing.T) {
	_, eng, _, s := speculationRig(t, nil)
	if s.Speculate(context.Background(), session.Transcript{Text: "stop", SpeakerID: "teagan"}) != nil {
		t.Error("speculated on a hot phrase")
	}
	sp := guess(t, s, "turn them off")
	ask := eng.asked(t)
	if s.Speculate(context.Background(), session.Transcript{Text: "turn them off please", SpeakerID: "teagan"}) != nil {
		t.Error("speculated twice at once")
	}
	go sp.Discard(session.DiscardResumed)
	ask.cut(t, "")

	turn := heardBy(s, "what's the weather tomorrow", nil)
	busy := eng.asked(t)
	if s.Speculate(context.Background(), session.Transcript{Text: "and on saturday", SpeakerID: "teagan"}) != nil {
		t.Error("speculated while a turn was answering")
	}
	busy.say(t, session.TurnEnd{FinishReason: "stop", Completion: `{}`})
	wait(t, turn)
}

// The speculative ask is told what Teagan remembers, as the turn would be.
// Kept, the recollection is recorded once, in the turn, and the memories are
// not asked for again.
//
// verifies SPEC §5, §11
func TestAKeptSpeculationRecordsWhatItWasToldItRemembers(t *testing.T) {
	mem := &remembered{byPerson: map[string][]journal.Memory{"teagan": {oatMilk, bins}}, summaries: map[string][]journal.Summary{}}
	r, eng, _, s := speculationRig(t, func(c *session.Config) { c.Memories = mem })
	asksBefore := len(mem.asked())
	recalledBefore := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled)

	// Teagan shared the bins in the last turn; now only the oat milk is hers.
	mem.set("teagan", oatMilk)
	sp := guess(t, s, "do I take milk")
	ask := eng.asked(t)
	if !reflect.DeepEqual(ask.in.Memories, []journal.Memory{oatMilk}) {
		t.Errorf("speculative ask told %v, want the oat milk", ask.in.Memories)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != recalledBefore {
		t.Fatal("the speculative recollection was written before the turn was confirmed")
	}
	turn := heardBy(s, "do I take milk", sp)
	ask.say(t, session.ToolCall{ID: "s3", Tool: "speak", Args: `{"text":"Oat milk."}`}, session.TurnEnd{FinishReason: "stop", Completion: `{}`})
	wait(t, turn)

	if n := len(mem.asked()) - asksBefore; n != 1 {
		t.Errorf("memories asked %d times for the turn, want once, in the pause", n)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled) - recalledBefore; n != 1 {
		t.Errorf("%d memory_recalled for the turn, want 1", n)
	}
	if st := r.state(t, s.ConversationID()); !reflect.DeepEqual(st.Recalled, []journal.Memory{oatMilk}) {
		t.Errorf("log says the turn was told %v, want the oat milk", st.Recalled)
	}
}
