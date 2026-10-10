package session_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

const (
	openGarage = `{"domain":"cover","service":"open_cover","entity_id":"cover.garage_door"}`
	openBlinds = `{"domain":"cover","service":"open_cover","entity_id":"cover.living_room_blinds"}`
)

// coverTool is Home Assistant's covers as the session sees them: it says
// what each cover is before a call runs, and records the calls that do.
type coverTool struct {
	classes map[string]string
	down    error

	mu     sync.Mutex
	asked  []string
	opened []string
}

func (c *coverTool) Classify(_ context.Context, args string) ([]string, error) {
	var a struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.asked = append(c.asked, a.EntityID)
	down := c.down
	c.mu.Unlock()
	if down != nil {
		return nil, down
	}
	if class, ok := c.classes[a.EntityID]; ok {
		return []string{class}, nil
	}
	return nil, nil
}

func (c *coverTool) Invoke(_ context.Context, args string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.opened = append(c.opened, args)
	return `[]`, nil
}

func (c *coverTool) seen() (asked, opened []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.asked...), append([]string(nil), c.opened...)
}

// teagansCovers: a garage door and the living-room blinds, as HA classes them.
func teagansCovers() *coverTool {
	return &coverTool{classes: map[string]string{
		"cover.garage_door":        "garage",
		"cover.living_room_blinds": "blind",
	}}
}

// coverModel opens whichever cover Teagan names, asks when a call is held,
// and calls again with the nonce once she says yes.
func coverModel(in session.Input) []session.Action {
	last := in.Dialogue[len(in.Dialogue)-1]
	switch {
	case last.Kind == journal.EntryHeard && last.Text == "open the garage door":
		return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: openGarage}, done}
	case last.Kind == journal.EntryHeard && last.Text == "open the living room blinds":
		return []session.Action{session.ToolCall{ID: "call_c1", Tool: "ha_call_service", Args: openBlinds}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "confirmation_required":
		return []session.Action{said("call_s1", "Open the garage door?"), done}
	case last.Kind == journal.EntryHeard && last.Text == "yes, open it":
		nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
		return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: withNonce(openGarage, nonce)}, done}
	case last.Kind == journal.EntryResult && last.Outcome == "ok":
		return []session.Action{said(fmt.Sprintf("call_s%d", len(in.Dialogue)), "Done."), done}
	}
	return []session.Action{done}
}

// Teagan opens the living-room blinds and nobody asks her anything: the
// blinds are a cover, but not one that lets anyone in.
//
// verifies SPEC §6
func TestTheBlindsOpenWithoutAQuestion(t *testing.T) {
	covers := teagansCovers()
	r := modelRig(t, &model{answer: coverModel}, map[string]session.Tool{"ha_call_service": covers})
	s := r.open(t, "teagan")

	wait(t, heard(s, "open the living room blinds"))

	asked, opened := covers.seen()
	if want := []string{"cover.living_room_blinds"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("classified %q, want the blinds once", asked)
	}
	if len(opened) != 1 || opened[0] != openBlinds {
		t.Errorf("opened %q, want the blinds once: %s", opened, openBlinds)
	}
	if requested, _ := r.confirmations(t, s.ConversationID()); len(requested) != 0 {
		t.Errorf("confirmations requested %v for the blinds", requested)
	}
	if st := r.state(t, s.ConversationID()); !reflect.DeepEqual(st.Spoken, []string{"Done."}) {
		t.Errorf("spoken = %q, want only %q", st.Spoken, "Done.")
	}
}

// The garage door is opened by the same call as the blinds. It is held for
// Teagan's yes, and the call that comes back with the nonce runs on the
// log's word, without asking Home Assistant again.
//
// verifies SPEC §6
func TestTheGarageDoorWaitsForTeagansYes(t *testing.T) {
	covers := teagansCovers()
	r := modelRig(t, &model{answer: coverModel}, map[string]session.Tool{"ha_call_service": covers})
	s := r.open(t, "teagan")

	wait(t, heard(s, "open the garage door"))
	if _, opened := covers.seen(); len(opened) != 0 {
		t.Fatalf("the garage was opened %q before Teagan said yes", opened)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("first call = %+v, want it held", c)
	}

	wait(t, heard(s, "yes, open it"))
	asked, opened := covers.seen()
	if want := `{"domain":"cover","entity_id":"cover.garage_door","service":"open_cover"}`; len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %q, want the garage once without the nonce: %s", opened, want)
	}
	if want := []string{"cover.garage_door"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("classified %q, want the garage once, before the question", asked)
	}
	st := r.state(t, s.ConversationID())
	if got := st.Confirmations; len(got) != 1 || got[0].Answer != "yes, open it" || got[0].RedeemedBy != "call_c2" {
		t.Errorf("confirmations = %+v, want Teagan's yes redeemed by call_c2", got)
	}
}

// Home Assistant cannot say what the cover is. The call is held: a gate
// that opens when it cannot see what it opens is not a gate.
//
// verifies SPEC §6
func TestACoverNobodyCanClassifyWaitsForAYes(t *testing.T) {
	covers := teagansCovers()
	covers.down = errors.New("get state cover.living_room_blinds: 503 Service Unavailable")
	r := modelRig(t, &model{answer: coverModel}, map[string]session.Tool{"ha_call_service": covers})
	s := r.open(t, "teagan")

	wait(t, heard(s, "open the living room blinds"))

	if _, opened := covers.seen(); len(opened) != 0 {
		t.Errorf("opened %q though nothing could say what it was", opened)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("call = %+v, want it held", c)
	}
}

// Home Assistant was down when Teagan asked for the blinds, so the call was
// held and she was asked. By the time she says yes it is back, and says
// they are blinds. Her yes still runs the call, and the nonce stays the
// orchestrator's: the blinds get the arguments they declare.
//
// verifies SPEC §6
func TestAYesStillRunsTheCallWhenTheTargetIsReadableAgain(t *testing.T) {
	covers := teagansCovers()
	covers.down = errors.New("get state cover.living_room_blinds: 503 Service Unavailable")
	m := &model{answer: func(in session.Input) []session.Action {
		if last := in.Dialogue[len(in.Dialogue)-1]; last.Kind == journal.EntryHeard && last.Text == "yes, open it" {
			nonce, _ := held(session.Input{Dialogue: in.Dialogue[:len(in.Dialogue)-2]})
			return []session.Action{session.ToolCall{ID: "call_c2", Tool: "ha_call_service", Args: withNonce(openBlinds, nonce)}, done}
		}
		return coverModel(in)
	}}
	r := modelRig(t, m, map[string]session.Tool{"ha_call_service": covers})
	s := r.open(t, "teagan")

	wait(t, heard(s, "open the living room blinds"))
	covers.mu.Lock()
	covers.down = nil
	covers.mu.Unlock()
	wait(t, heard(s, "yes, open it"))

	_, opened := covers.seen()
	if want := `{"domain":"cover","entity_id":"cover.living_room_blinds","service":"open_cover"}`; len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened %q, want the blinds once without the nonce: %s", opened, want)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c2"); c.Outcome != "ok" {
		t.Errorf("confirmed call = %+v, want ok", c)
	}
	if _, given := r.confirmations(t, s.ConversationID()); len(given) != 1 || given[0]["call_id"] != "call_c2" {
		t.Errorf("given = %v, want call_c2 to redeem Teagan's yes", given)
	}
}

// Home Assistant takes the request for the garage's class and never
// answers. The read gets the tool's own timeout, as the call itself would,
// and then the call is held rather than run unread.
//
// verifies SPEC §6, §7
func TestAClassThatNeverComesHoldsTheCallAtTheToolsTimeout(t *testing.T) {
	stalled := &stalledCovers{coverTool: teagansCovers()}
	r := modelRig(t, &model{answer: coverModel}, map[string]session.Tool{"ha_call_service": stalled})
	s := r.open(t, "teagan")

	errc := heard(s, "open the living room blinds")
	timeout := registry.Specs["ha_call_service"].Timeout
	deadline := time.Now().Add(patience)
	for !slices.Contains(r.clock.waits(), timeout) {
		if time.Now().After(deadline) {
			t.Fatalf("no %v timer armed for the read; armed %v", timeout, r.clock.waits())
		}
		runtime.Gosched()
	}
	r.clock.advance(timeout)
	wait(t, errc)

	if _, opened := stalled.seen(); len(opened) != 0 {
		t.Errorf("opened %q though nothing said what it was", opened)
	}
	if c := callByID(t, r.state(t, s.ConversationID()), "call_c1"); c.Outcome != "confirmation_required" {
		t.Errorf("call = %+v, want it held", c)
	}
	if !stalled.abandoned() {
		t.Error("the read was left running after its timeout")
	}
}

// stalledCovers never says what a cover is; it waits until the read is
// abandoned.
type stalledCovers struct {
	*coverTool
	gaveUp atomic.Bool
}

func (c *stalledCovers) Classify(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	c.gaveUp.Store(true)
	return nil, ctx.Err()
}

func (c *stalledCovers) abandoned() bool {
	deadline := time.Now().Add(patience)
	for !c.gaveUp.Load() && time.Now().Before(deadline) {
		runtime.Gosched()
	}
	return c.gaveUp.Load()
}
