package memory_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/memory"
	"github.com/teaganglenn/chorus/internal/session"
)

func call(person, callID string) context.Context {
	return session.WithCaller(context.Background(), session.Caller{
		Person: person, ConversationID: "conv-kitchen-1", CallID: callID,
	})
}

// Teagan tells the kitchen about the oat milk. The memory is Teagan's because
// Teagan was speaking; the model never names whose it is.
//
// verifies SPEC §5
func TestRememberKeepsTheFactForWhoeverSaidIt(t *testing.T) {
	store := memory.NewMemStore()
	tools := memory.Tools(store, journal.FixedClock(breakfast))

	out, err := tools["remember"].Invoke(call("teagan", "call_r1"), `{"fact":"  Takes oat milk in coffee. "}`)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	var res struct{ Remembered string }
	if err := json.Unmarshal([]byte(out), &res); err != nil || !strings.HasPrefix(res.Remembered, "m_") {
		t.Fatalf("result %s: want the new memory's id", out)
	}
	got, err := store.Recall(context.Background(), "teagan", memory.RecallLimit)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	want := []memory.Memory{{
		ID: res.Remembered, Person: "teagan", Fact: "Takes oat milk in coffee.",
		ConversationID: "conv-kitchen-1", CallID: "call_r1", At: breakfast,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stored %+v, want %+v", got, want)
	}
}

// Shared only when the person said so.
//
// verifies SPEC §5
func TestASharedFactReachesTheRestOfTheHousehold(t *testing.T) {
	store := memory.NewMemStore()
	tools := memory.Tools(store, journal.FixedClock(breakfast))
	if _, err := tools["remember"].Invoke(call("alice", "call_r1"), `{"fact":"The guest wifi password is on the fridge.","shareable":true}`); err != nil {
		t.Fatalf("remember: %v", err)
	}
	if _, err := tools["remember"].Invoke(call("alice", "call_r2"), `{"fact":"Teagan's surprise party is Saturday at the Lantern."}`); err != nil {
		t.Fatalf("remember: %v", err)
	}
	rec, err := memory.Recaller(store, memory.RecallConfig{}).Recall(context.Background(), session.Ask{Person: "teagan", ConversationID: "conv-kitchen-2", Now: breakfast})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	got := rec.Memories
	if len(got) != 1 || got[0].Fact != "The guest wifi password is on the fridge." || !got[0].Shareable || got[0].Person != "alice" {
		t.Errorf("teagan recalls %+v, want only Alice's shared password", got)
	}
}

// Teagan can forget the oat milk, and cannot forget Alice's password for
// her. The refusal reads the same as for an id nobody has.
//
// verifies SPEC §5
func TestForgetTakesOnlyTheCallersOwn(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, household())
	forget := memory.Tools(store, journal.FixedClock(breakfast))["forget"]

	if out, err := forget.Invoke(call("teagan", "call_f1"), `{"memory_id":"m_3f9c2a10"}`); err != nil || out != `{"forgotten":"m_3f9c2a10"}` {
		t.Errorf("forgetting the oat milk = %s, %v", out, err)
	}
	_, theirs := forget.Invoke(call("teagan", "call_f2"), `{"memory_id":"m_77d01b2e"}`)
	_, nobodys := forget.Invoke(call("teagan", "call_f3"), `{"memory_id":"m_00000000"}`)
	if theirs == nil || nobodys == nil {
		t.Fatalf("refusals = %v, %v; want both refused", theirs, nobodys)
	}
	if strings.Contains(theirs.Error(), "alice") {
		t.Errorf("the refusal %q says whose the memory is", theirs)
	}
	got, err := store.Recall(context.Background(), "teagan", memory.RecallLimit)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if want := []string{"m_77d01b2e", "m_51ab0c3d"}; !reflect.DeepEqual(ids(got), want) {
		t.Errorf("teagan recalls %v, want %v", ids(got), want)
	}
}

// The session refuses a guest before the executor runs; the executor does
// not count on it. A blank fact is not a memory either.
//
// verifies SPEC §5
func TestAGuestOrABlankFactIsRefused(t *testing.T) {
	store := memory.NewMemStore()
	tools := memory.Tools(store, journal.FixedClock(breakfast))
	cases := []struct {
		why  string
		ctx  context.Context
		tool string
		args string
	}{
		{"a guest remembering", call("", "call_r1"), "remember", `{"fact":"Takes oat milk in coffee."}`},
		{"a call outside any session", context.Background(), "remember", `{"fact":"Takes oat milk in coffee."}`},
		{"a guest forgetting", call("", "call_f1"), "forget", `{"memory_id":"m_3f9c2a10"}`},
		{"nothing to remember", call("teagan", "call_r1"), "remember", `{"fact":"  "}`},
		{"arguments nobody can read", call("teagan", "call_r1"), "remember", `{"fact":`},
	}
	for _, c := range cases {
		if out, err := tools[c.tool].Invoke(c.ctx, c.args); err == nil {
			t.Errorf("%s: %s, want refused", c.why, out)
		}
	}
	if got, _ := store.Recall(context.Background(), "teagan", memory.RecallLimit); len(got) != 0 {
		t.Errorf("stored %+v from refused calls", got)
	}
}

// A guest recalls nothing, and without an embedding model a busy
// household's oldest memories make room for the newest.
//
// verifies SPEC §5
func TestTheRecallerGivesAGuestNothingAndAPersonTheNewest(t *testing.T) {
	store := memory.NewMemStore()
	rememberAll(t, store, household())
	r := memory.Recaller(store, memory.RecallConfig{})
	if got, err := r.Recall(context.Background(), session.Ask{Person: "", ConversationID: "conv-front-door-1", Now: breakfast}); err != nil || got.Memories != nil || got.Summaries != nil {
		t.Errorf("a guest recalls %+v, %v; want nothing", got, err)
	}

	tools := memory.Tools(store, journal.FixedClock(breakfast.Add(24*time.Hour)))
	for i := range memory.RecallLimit {
		if _, err := tools["remember"].Invoke(call("teagan", "call_r"+strconv.Itoa(i)), `{"fact":"Practised piano today."}`); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
	rec, err := r.Recall(context.Background(), session.Ask{Person: "teagan", ConversationID: "conv-kitchen-2", Now: breakfast.Add(24 * time.Hour)})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	got := rec.Memories
	if len(got) != memory.RecallLimit {
		t.Fatalf("recalled %d, want the limit of %d", len(got), memory.RecallLimit)
	}
	for _, m := range got {
		if m.ID == "m_3f9c2a10" {
			t.Error("the oldest memory was recalled over the newest")
		}
	}
}
