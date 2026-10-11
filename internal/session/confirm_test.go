package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

const (
	unlockFrontDoor = `{"domain":"lock","service":"unlock","entity_id":"lock.front_door"}`
	unlockBackDoor  = `{"domain":"lock","service":"unlock","entity_id":"lock.back_door"}`
	frontDoorOpened = `[{"entity_id":"lock.front_door","state":"unlocked"}]`
)

// model is a turn engine that answers from what it is told, as a model
// would: each ask is decided by the dialogue it carries.
type model struct {
	answer func(in session.Input) []session.Action

	mu     sync.Mutex
	inputs []session.Input
}

func (m *model) Turn(_ context.Context, in session.Input) (<-chan session.Action, error) {
	m.mu.Lock()
	m.inputs = append(m.inputs, in)
	m.mu.Unlock()
	acts := m.answer(in)
	out := make(chan session.Action, len(acts))
	for _, a := range acts {
		out <- a
	}
	close(out)
	return out, nil
}

func (m *model) asks() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.inputs)
}

func said(id, text string) session.Action {
	return session.SpeechDelta{CallID: id, Text: text, Last: true}
}

var done = session.TurnEnd{FinishReason: "stop", Completion: "{}"}

// held is the nonce the newest held call in the dialogue was handed, if the
// dialogue's last result is one.
func held(in session.Input) (string, bool) {
	for i := len(in.Dialogue) - 1; i >= 0; i-- {
		e := in.Dialogue[i]
		if e.Kind != journal.EntryResult {
			continue
		}
		if e.Outcome != "confirmation_required" {
			return "", false
		}
		var r struct {
			Nonce string `json:"nonce"`
		}
		if err := json.Unmarshal([]byte(e.Result), &r); err != nil {
			return "", false
		}
		return r.Nonce, true
	}
	return "", false
}

func withNonce(args, nonce string) string {
	return strings.TrimSuffix(args, "}") + `,"confirmation":"` + nonce + `"}`
}

// frontDoorModel unlocks the front door when asked, asks Teagan when the call
// is held, and calls again with the nonce once she answers.
func frontDoorModel(in session.Input) []session.Action {
	last := in.Dialogue[len(in.Dialogue)-1]
	switch {
	case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
		return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlockFrontDoor}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
		return []session.Action{said("call_s1", "Unlock the front door?"), done}
	case last.Kind == journal.EntryHeard && last.Text == "yes please":
		// The question is two steps back: the held call's result, then the
		// spoken question, then her answer.
		nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
		return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: withNonce(unlockFrontDoor, nonce)}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "ok":
		return []session.Action{said("call_s2", "Done, the front door is unlocked."), done}
	}
	return []session.Action{done}
}

// lockTool records the arguments Home Assistant would have been sent.
type lockTool struct {
	mu   sync.Mutex
	sent []string
}

func (l *lockTool) Invoke(_ context.Context, args string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, args)
	return frontDoorOpened, nil
}

func (l *lockTool) calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.sent...)
}

func modelRig(t *testing.T, m *model, tools map[string]session.Tool) *rig {
	t.Helper()
	return newRigWith(t, nil, tools, nil, func(c *session.Config) { c.Engine = m })
}

// confirmations lists the nonces the session handed out and the ones it
// accepted, as the log recorded them, in order.
func (r *rig) confirmations(t *testing.T, convID string) (requested, given []map[string]string) {
	t.Helper()
	events, err := r.store.Events(context.Background(), convID)
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for _, e := range events {
		switch e.Kind {
		case journal.KindConfirmationRequested:
			requested = append(requested, e.Fields)
		case journal.KindConfirmationGiven:
			given = append(given, e.Fields)
		}
	}
	return requested, given
}

// Teagan asks the kitchen to unlock the front door. The call is held until
// she has said yes: the model asks, she answers, and only the call that
// comes back with the nonce reaches the lock, without the nonce.
//
// verifies SPEC §6
func TestUnlockingTheFrontDoorWaitsForTeagansYes(t *testing.T) {
	lock := &lockTool{}
	m := &model{answer: frontDoorModel}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, heard(s, "unlock the front door"))
	if got := lock.calls(); len(got) != 0 {
		t.Fatalf("the lock was sent %q before Teagan said yes", got)
	}
	st := r.state(t, s.ConversationID())
	if c := callByID(t, st, "call_c1"); c.Outcome != "confirmation_required" || !strings.Contains(c.Result, `"nonce":"cf_`) {
		t.Errorf("held call = %+v, want confirmation_required with a nonce", c)
	}
	if want := []string{"Unlock the front door?"}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want the model's question %q", st.Spoken, want)
	}

	wait(t, heard(s, "yes please"))
	got := lock.calls()
	if want := `{"domain":"lock","entity_id":"lock.front_door","service":"unlock"}`; len(got) != 1 || got[0] != want {
		t.Fatalf("the lock was sent %q, want the front door once without the nonce: %s", got, want)
	}
	st = r.state(t, s.ConversationID())
	if c := callByID(t, st, "call_c2"); c.Outcome != "ok" {
		t.Errorf("confirmed call = %+v, want ok", c)
	}
	if want := []string{"Unlock the front door?", "Done, the front door is unlocked."}; !reflect.DeepEqual(st.Spoken, want) {
		t.Errorf("spoken = %q, want %q", st.Spoken, want)
	}

	requested, given := r.confirmations(t, s.ConversationID())
	if len(requested) != 1 || len(given) != 1 || given[0]["nonce"] != requested[0]["nonce"] || given[0]["call_id"] != "call_c2" {
		t.Errorf("requested %v, given %v: want call_c2 to redeem the one nonce", requested, given)
	}
	if c := st.Confirmations[0]; c.Answer != "yes please" || c.RedeemedBy != "call_c2" {
		t.Errorf("confirmation = %+v, want answered %q and redeemed by call_c2", c, "yes please")
	}
}

// A model that gets the nonce and calls straight back with it has asked
// nobody. Every such call is held again, until the round cap ends the turn.
//
// verifies SPEC §6
func TestAModelCannotSayYesForThePerson(t *testing.T) {
	lock := &lockTool{}
	n := 0
	m := &model{answer: func(in session.Input) []session.Action {
		n++
		nonce, ok := held(in)
		if !ok {
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlockFrontDoor}, done}
		}
		return []session.Action{session.ToolCall{ID: fmt.Sprintf("call_c%d", n), Tool: "ha_call_service", Args: withNonce(unlockFrontDoor, nonce)}, done}
	}}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, heard(s, "unlock the front door"))

	if got := lock.calls(); len(got) != 0 {
		t.Errorf("the lock was sent %q with nobody asked", got)
	}
	if m.asks() != session.DefaultRounds {
		t.Errorf("model asked %d times, want the round cap %d", m.asks(), session.DefaultRounds)
	}
	requested, given := r.confirmations(t, s.ConversationID())
	if len(given) != 0 {
		t.Errorf("confirmations given: %v", given)
	}
	for i, f := range requested[1:] {
		if f["refused"] != journal.RefusedNotAnswered {
			t.Errorf("re-call %d refused %q, want %q", i+1, f["refused"], journal.RefusedNotAnswered)
		}
	}
}

// Teagan said yes to the front door. A model that then reaches for the back
// door with the same nonce is held and asked to ask again.
//
// verifies SPEC §6
func TestAYesToTheFrontDoorDoesNotOpenTheBackDoor(t *testing.T) {
	lock := &lockTool{}
	m := &model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Text == "yes please" {
			nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: withNonce(unlockBackDoor, nonce)}, done}
		}
		if _, ok := held(in); ok {
			return []session.Action{said(fmt.Sprintf("call_s%d", len(in.Dialogue)), "Unlock it?"), done}
		}
		return frontDoorModel(in)
	}}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, heard(s, "unlock the front door"))
	wait(t, heard(s, "yes please"))

	if got := lock.calls(); len(got) != 0 {
		t.Errorf("the lock was sent %q on a yes to another door", got)
	}
	requested, _ := r.confirmations(t, s.ConversationID())
	if len(requested) != 2 || requested[1]["refused"] != journal.RefusedArgsChanged {
		t.Errorf("requested = %v, want the back door refused as %s", requested, journal.RefusedArgsChanged)
	}
}

// The model switches from the front door to the back door before Teagan has
// answered, and asks about the back door. Her yes is to the back door, so
// the front door's nonce, refused when it was presented for the back door,
// must not open the front door.
//
// verifies SPEC §6
func TestAYesToTheBackDoorQuestionDoesNotOpenTheFrontDoor(t *testing.T) {
	lock := &lockTool{}
	var front string
	m := &model{answer: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		nonce, isHeld := held(in)
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlockFrontDoor}, done}
		case last.Kind == journal.EntryHeard && last.Text == "yes please":
			return []session.Action{session.ToolCall{ID: "call_c3", Tool: "ha_call_service", Args: withNonce(unlockFrontDoor, front)}, done}
		case isHeld && front == "":
			front = nonce
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: withNonce(unlockBackDoor, nonce)}, done}
		case isHeld:
			return []session.Action{said("call_s1", "Unlock the back door?"), done}
		}
		return []session.Action{done}
	}}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, heard(s, "unlock the front door"))
	wait(t, heard(s, "yes please"))

	if got := lock.calls(); len(got) != 0 {
		t.Errorf("the lock was sent %q on a yes to a different question", got)
	}
	requested, given := r.confirmations(t, s.ConversationID())
	if len(given) != 0 {
		t.Errorf("confirmations given: %v", given)
	}
	if last := requested[len(requested)-1]; last["presented"] != front || last["refused"] != journal.RefusedUsed {
		t.Errorf("front door re-call = %v, want %s refused as %s", last, front, journal.RefusedUsed)
	}
}

// The kitchen lights are not a front door. They run on the call, with no
// nonce in the log.
//
// verifies SPEC §6
func TestTheKitchenLightsDoNotWaitForAYes(t *testing.T) {
	lights := &lockTool{}
	m := &model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: `{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}`}, done}
		}
		return []session.Action{said("call_s1", "Kitchen lights are on."), done}
	}}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lights})
	s := r.open(t, "alan")

	wait(t, heard(s, "turn on the kitchen lights"))

	if got := lights.calls(); len(got) != 1 || got[0] != `{"domain":"light","service":"turn_on","entity_id":"light.kitchen"}` {
		t.Errorf("home assistant was sent %q, want the kitchen lights as the model called them", got)
	}
	if requested, _ := r.confirmations(t, s.ConversationID()); len(requested) != 0 {
		t.Errorf("the kitchen lights were held: %v", requested)
	}
}

// A tool that requires confirmation holds every call, whatever its
// arguments: the garage door opener on its own relay.
//
// verifies SPEC §6
func TestAToolThatAlwaysNeedsAYesHoldsEveryCall(t *testing.T) {
	opener := &lockTool{}
	specs := map[string]registry.ToolSpec{
		"speak":       registry.Specs["speak"],
		"open_garage": {Name: "open_garage", OnInterrupt: registry.InterruptUninterruptible, RequiresConfirmation: true, Timeout: registry.Specs["speak"].Timeout},
	}
	m := &model{answer: func(in session.Input) []session.Action {
		if _, ok := held(in); ok {
			return []session.Action{said("call_s1", "Open the garage?"), done}
		}
		return []session.Action{session.ToolCall{ID: "call_c1", Tool: "open_garage", Args: `{}`}, done}
	}}
	r := newRigWith(t, nil, map[string]session.Tool{"open_garage": opener}, specs, func(c *session.Config) { c.Engine = m })
	s := r.open(t, "alan")

	wait(t, heard(s, "open the garage"))

	if got := opener.calls(); len(got) != 0 {
		t.Errorf("the garage opener ran on %q without a yes", got)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("garage call = %+v, want it held", c)
	}
}

// held writes into the log a call the session held earlier, as if the
// person had been asked and is about to answer.
func (r *rig) held(t *testing.T, convID, callID, tool, args, nonce string) {
	t.Helper()
	j := journal.New(r.store, r.clock, versions())
	for _, rec := range []journal.Record{
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": tool, "call_id": callID, "args_json": args}},
		{Kind: journal.KindConfirmationRequested, Fields: map[string]string{"call_id": callID, "nonce": nonce}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": callID, "outcome": "confirmation_required"}},
	} {
		if _, err := j.Append(context.Background(), convID, rec); err != nil {
			t.Fatalf("append %s: %v", rec.Kind, err)
		}
	}
}

// refusingStore loses every write of one kind.
type refusingStore struct {
	journal.Store
	kind journal.Kind
}

var errDiskFull = errors.New("journal: no space left on device")

func (r refusingStore) Append(ctx context.Context, e journal.Event) error {
	if e.Kind == r.kind {
		return errDiskFull
	}
	return r.Store.Append(ctx, e)
}

// If the log cannot say the nonce was spent, it could be spent again, so the
// front door stays locked and the turn reports the lost write.
//
// verifies SPEC §6, §8
func TestAYesTheLogCannotKeepOpensNothing(t *testing.T) {
	lock := &lockTool{}
	m := &model{answer: frontDoorModel}
	r := newRigWith(t, nil, map[string]session.Tool{"ha_call_service": lock}, nil, func(c *session.Config) {
		c.Engine = m
		c.Store = refusingStore{Store: c.Store, kind: journal.KindConfirmationGiven}
		c.Journal = journal.New(c.Store, c.Clock, versions())
	})
	s := r.open(t, "teagan")

	wait(t, heard(s, "unlock the front door"))
	err := s.Heard(context.Background(), session.Transcript{Text: "yes please", SpeakerID: "teagan", AudioRef: "blob://mic/2"})
	if !errors.Is(err, errDiskFull) {
		t.Errorf("turn error = %v, want the lost write", err)
	}
	if got := lock.calls(); len(got) != 0 {
		t.Errorf("the lock was sent %q on a yes the log never kept", got)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c2"); c.Outcome != "error" {
		t.Errorf("call = %+v, want it failed", c)
	}
}

// askerModel is frontDoorModel for a room with more than one voice in it:
// whenever anyone says yes it calls the lock again with the newest nonce,
// each call under its own id, and asks again whenever it is held.
func askerModel() func(session.Input) []session.Action {
	n := 1
	return func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryHeard && last.Text == "unlock the front door":
			return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: unlockFrontDoor}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
			return []session.Action{said(fmt.Sprintf("call_s%d", len(in.Dialogue)), "Unlock the front door?"), done}
		case last.Kind == journal.EntryHeard && strings.HasPrefix(last.Text, "yes"):
			nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
			n++
			return []session.Action{session.ToolCall{ID: fmt.Sprintf("call_c%d", n), Tool: "ha_call_service", Args: withNonce(unlockFrontDoor, nonce)}, done}
		case last.Kind == journal.EntryResult && last.Outcome == "ok":
			return []session.Action{said("call_s_done", "Done, the front door is unlocked."), done}
		}
		return []session.Action{done}
	}
}

// refusedThenRedeemed checks the log of a front door asked for once,
// refused once for why, and unlocked on the yes that followed.
func refusedThenRedeemed(t *testing.T, r *rig, convID, why string) {
	t.Helper()
	requested, given := r.confirmations(t, convID)
	if len(requested) != 2 || requested[1]["refused"] != why || requested[1]["presented"] != requested[0]["nonce"] {
		t.Errorf("requested = %v, want the first nonce refused as %s and a fresh one handed out", requested, why)
	}
	if len(given) != 1 || given[0]["nonce"] != requested[1]["nonce"] || given[0]["call_id"] != "call_c3" {
		t.Errorf("given = %v, want call_c3 to redeem the fresh nonce", given)
	}
	st := r.state(t, convID)
	for _, c := range st.Confirmations {
		if c.AskedBy != "teagan" {
			t.Errorf("confirmation %+v, want Teagan's question", c)
		}
	}
}

// Teagan asks the kitchen to unlock the front door, and Alice, from across
// the room, says yes. The question was Teagan's: the lock is not sent, the
// model is told someone else answered and handed a fresh nonce, and Teagan's
// own yes then unlocks the door.
//
// verifies SPEC §5, §6
func TestAlicesYesDoesNotUnlockWhatTeaganAsked(t *testing.T) {
	lock := &lockTool{}
	m := &model{answer: askerModel()}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, voiced(s, "unlock the front door", "teagan", "identified"))
	wait(t, voiced(s, "yes please", "alice", "identified"))
	if got := lock.calls(); len(got) != 0 {
		t.Fatalf("the lock was sent %q on Alice's yes to Teagan's question", got)
	}
	c := callByID(t, r.state(t, s.ConversationID()), "call_c2")
	if c.Outcome != "confirmation_required" || !strings.Contains(c.Result, `"refused":"wrong_person"`) || !strings.Contains(c.Result, "someone else answered") {
		t.Errorf("Alice's yes = %+v, want it refused as wrong_person with a note saying someone else answered", c)
	}
	if strings.Contains(c.Result, "alice") || strings.Contains(c.Result, "teagan") {
		t.Errorf("the model was told who: %s", c.Result)
	}

	wait(t, voiced(s, "yes, go ahead", "teagan", "identified"))
	if got := lock.calls(); len(got) != 1 || got[0] != `{"domain":"lock","entity_id":"lock.front_door","service":"unlock"}` {
		t.Fatalf("the lock was sent %q, want the front door once on Teagan's yes", got)
	}
	refusedThenRedeemed(t, r, s.ConversationID(), journal.RefusedWrongPerson)
}

// A dinner guest says yes to Teagan's question. Their voice matched nobody
// the household enrolled, so the lock is not sent and the model is told a
// guest cannot confirm it; Teagan's yes then unlocks the door.
//
// verifies SPEC §5, §6
func TestAGuestsYesUnlocksNothing(t *testing.T) {
	lock := &lockTool{}
	m := &model{answer: askerModel()}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": lock})
	s := r.open(t, "teagan")

	wait(t, voiced(s, "unlock the front door", "teagan", "identified"))
	wait(t, voiced(s, "yes", "", "below_threshold"))
	if got := lock.calls(); len(got) != 0 {
		t.Fatalf("the lock was sent %q on a guest's yes", got)
	}
	c := callByID(t, r.state(t, s.ConversationID()), "call_c2")
	if c.Outcome != "confirmation_required" || !strings.Contains(c.Result, `"refused":"guest"`) || !strings.Contains(c.Result, "not enrolled") {
		t.Errorf("the guest's yes = %+v, want it refused as guest with a note saying so", c)
	}

	wait(t, voiced(s, "yes please", "teagan", "identified"))
	if got := lock.calls(); len(got) != 1 {
		t.Fatalf("the lock was sent %q, want the front door once on Teagan's yes", got)
	}
	refusedThenRedeemed(t, r, s.ConversationID(), journal.RefusedGuest)
}
