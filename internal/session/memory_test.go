package session_test

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/session"
)

// remembered is the household's memory as a test sets it: what each person
// recalls right now, and the summaries the session asked it to keep.
type remembered struct {
	mu        sync.Mutex
	byPerson  map[string][]journal.Memory
	summaries map[string][]journal.Summary
	err       error
	keepErr   error
	rankedBy  string
	asks      []session.Ask
	kept      []kept
}

// kept is one Keep call: a conversation's summary and whom it was kept for.
type kept struct {
	People  []string
	Summary journal.Summary
}

func (r *remembered) Recall(_ context.Context, a session.Ask) (session.Recollection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks = append(r.asks, a)
	return session.Recollection{Memories: r.byPerson[a.Person], Summaries: r.summaries[a.Person], RankedBy: r.rankedBy}, r.err
}

func (r *remembered) asked() []session.Ask {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asks)
}

func (r *remembered) Keep(_ context.Context, people []string, s journal.Summary) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keepErr != nil {
		return r.keepErr
	}
	r.kept = append(r.kept, kept{People: slices.Clone(people), Summary: s})
	return nil
}

func (r *remembered) keeps() []kept {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.kept)
}

func (r *remembered) set(person string, ms ...journal.Memory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byPerson[person] = ms
}

func (r *remembered) setSummaries(person string, ss ...journal.Summary) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.summaries[person] = ss
}

var (
	oatMilk = journal.Memory{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee."}
	wifi    = journal.Memory{ID: "m_77d01b2e", Person: "alice", Fact: "The guest wifi password is on the fridge.", Shareable: true}
	bins    = journal.Memory{ID: "m_51ab0c3d", Person: "teagan", Fact: "The bins go out on Thursday night.", Shareable: true}
)

func newMemoryRig(t *testing.T, steps []step, tools map[string]session.Tool, specs map[string]registry.ToolSpec) (*rig, *remembered) {
	t.Helper()
	mem := &remembered{byPerson: map[string][]journal.Memory{
		"teagan": {oatMilk, wifi},
		"alice":  {wifi},
	}, summaries: map[string][]journal.Summary{}}
	r := newRigWith(t, steps, tools, specs, func(c *session.Config) { c.Memories = mem })
	return r, mem
}

// Teagan asks the kitchen something; the model is told Teagan takes oat milk
// and that the wifi password is on the fridge, and the log says so.
//
// verifies SPEC §5, §8
func TestATurnIsToldWhatItsSpeakerRemembers(t *testing.T) {
	steps := []step{
		{act: session.SpeechDelta{CallID: "s1", Text: "Oat milk, as always.", Last: true}},
		{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}},
	}
	r, mem := newMemoryRig(t, steps, nil, nil)
	s := r.open(t, "teagan")
	wait(t, heard(s, "how do I take my coffee"))

	asks := r.engine.asks()
	if len(asks) != 1 || !reflect.DeepEqual(asks[0].Memories, []journal.Memory{oatMilk, wifi}) {
		t.Fatalf("the model was asked with %+v, want Teagan's oat milk and the shared password", asks)
	}
	st := r.state(t, s.ConversationID())
	if !reflect.DeepEqual(st.Recalled, asks[0].Memories) || st.RecalledFor != "teagan" {
		t.Errorf("the log recalled %+v for %q, not what the model was given", st.Recalled, st.RecalledFor)
	}

	// Nothing changed: the next turn recalls again but records nothing new.
	wait(t, heard(s, "and is it bin night"))
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 1 {
		t.Errorf("memory_recalled recorded %d times for an unchanged memory, want once", n)
	}

	// Teagan shares bin night; the turn after is told.
	mem.set("teagan", bins, oatMilk, wifi)
	wait(t, heard(s, "what else do you know"))
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 2 {
		t.Errorf("memory_recalled recorded %d times, want again once the memory changed", n)
	}
	asks = r.engine.asks()
	if got := asks[len(asks)-1].Memories; !reflect.DeepEqual(got, []journal.Memory{bins, oatMilk, wifi}) {
		t.Errorf("the last ask was told %+v, want bin night first", got)
	}
}

// Teagan asks about the garage, and the memories are chosen for it by an
// embedding model: the log says which, and what was said that they were
// chosen for reaches the recall. When the model is down for the next turn
// and the same memories are the newest, the log says they were not ranked.
//
// verifies SPEC §5, §8
func TestARecallSaysWhatChoseIt(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, mem := newMemoryRig(t, steps, nil, nil)
	mem.rankedBy = "nomic-embed-text"
	s := r.open(t, "teagan")
	wait(t, heard(s, "what's the code for the garage"))

	recalls := func() []journal.Event {
		var out []journal.Event
		for _, e := range r.events(t, s.ConversationID()) {
			if e.Kind == journal.KindMemoryRecalled {
				out = append(out, e)
			}
		}
		return out
	}
	if got := recalls(); len(got) != 1 || got[0].Fields["ranked_by"] != "nomic-embed-text" {
		t.Fatalf("recalls %+v, want one ranked by the embedding model", got)
	}
	if st := r.state(t, s.ConversationID()); st.RecalledRankedBy != "nomic-embed-text" {
		t.Errorf("state ranked by %q, want the embedding model", st.RecalledRankedBy)
	}
	if got := mem.asked(); len(got) != 1 || got[0].Words != "what's the code for the garage" {
		t.Errorf("recalled for %+v, want what Teagan said", got)
	}

	mem.mu.Lock()
	mem.rankedBy = ""
	mem.mu.Unlock()
	wait(t, heard(s, "and the front door"))
	got := recalls()
	if len(got) != 2 {
		t.Fatalf("recorded %d recalls, want again once ranking stopped", len(got))
	}
	if _, ok := got[1].Fields["ranked_by"]; ok {
		t.Errorf("an unranked recall says %v", got[1].Fields)
	}
}

// A guest is told nothing, and nobody's memory is looked up for them.
//
// verifies SPEC §5
func TestAGuestIsToldNothingRemembered(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, mem := newMemoryRig(t, steps, nil, nil)
	s := r.open(t, "")
	wait(t, heard(s, "what's the wifi password"))

	if asks := r.engine.asks(); len(asks) != 1 || asks[0].Memories != nil {
		t.Errorf("a guest's turn was told %+v", asks)
	}
	if got := mem.asked(); len(got) != 0 {
		t.Errorf("recalled for %+v on a guest's turn", got)
	}
	if n := countKind(r.kinds(t, s.ConversationID()), journal.KindMemoryRecalled); n != 0 {
		t.Errorf("memory_recalled recorded %d times for a guest", n)
	}
}

// A turn that cannot recall fails rather than answering as if nothing were
// remembered: the store is the journal's database, so the log is in trouble
// too.
//
// verifies SPEC §5, §8
func TestATurnThatCannotRecallFails(t *testing.T) {
	steps := []step{{act: session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r, mem := newMemoryRig(t, steps, nil, nil)
	mem.err = errors.New("connection refused")
	s := r.open(t, "teagan")
	if err := <-heard(s, "how do I take my coffee"); err == nil {
		t.Error("the turn answered without its memories")
	}
	if asks := r.engine.asks(); len(asks) != 0 {
		t.Errorf("the model was asked %d times", len(asks))
	}
}

// An unidentified voice asking to be remembered is refused before anything
// runs, and the model hears why.
//
// verifies SPEC §5
func TestAGuestCannotRememberOrForget(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"Takes oat milk in coffee."}`}},
		{act: session.ToolCall{ID: "call_f1", Tool: "forget", Args: `{"memory_id":"m_3f9c2a10"}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	ran := make(chan string, 2)
	tool := session.ToolFunc(func(_ context.Context, args string) (string, error) {
		ran <- args
		return `{"ok":true}`, nil
	})
	r, _ := newMemoryRig(t, steps, map[string]session.Tool{"remember": tool, "forget": tool}, nil)
	s := r.open(t, "")
	wait(t, heard(s, "remember that I take oat milk"))

	st := r.state(t, s.ConversationID())
	for _, id := range []string{"call_r1", "call_f1"} {
		if c := callByID(t, st, id); c.Outcome != "error" || c.Result != `{"error":"unidentified_speaker"}` {
			t.Errorf("%s = %+v, want refused as unidentified", id, c)
		}
	}
	if len(ran) != 0 {
		t.Errorf("a guest's call reached the executor: %s", <-ran)
	}
}

// The executor learns who the call is for from the session, not from what
// the model wrote.
//
// verifies SPEC §5
func TestAPersonScopedCallKnowsWhoMadeIt(t *testing.T) {
	steps := []step{
		{act: session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"Takes oat milk in coffee."}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	callers := make(chan session.Caller, 1)
	tool := session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
		c, _ := session.CallerFrom(ctx)
		callers <- c
		return `{"remembered":"m_3f9c2a10"}`, nil
	})
	r, _ := newMemoryRig(t, steps, map[string]session.Tool{"remember": tool}, nil)
	s := r.open(t, "teagan")
	wait(t, heard(s, "remember that I take oat milk"))

	want := session.Caller{Person: "teagan", ConversationID: s.ConversationID(), CallID: "call_r1", Satellite: "kitchen"}
	if got := <-callers; got != want {
		t.Errorf("caller = %+v, want %+v", got, want)
	}
}

// A guest-scoped tool runs in guest context whoever is speaking: it is told
// nobody's identity, so it can reach nobody's memories (SPEC §5). No shipped
// tool declares the scope yet; a forecast service is the shape of one.
//
// verifies SPEC §5
func TestAGuestScopedToolRunsAsAGuestForEveryone(t *testing.T) {
	specs := maps.Clone(registry.Specs)
	specs["weather_forecast"] = registry.ToolSpec{
		Name: "weather_forecast", OnInterrupt: registry.InterruptCancel,
		Scope: registry.ScopeGuest, Timeout: registry.Specs["remember"].Timeout,
	}
	steps := []step{
		{act: session.ToolCall{ID: "call_w1", Tool: "weather_forecast", Args: `{}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	callers := make(chan session.Caller, 1)
	tool := session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
		c, _ := session.CallerFrom(ctx)
		callers <- c
		return `{"today":"sunny, 21C"}`, nil
	})
	r, _ := newMemoryRig(t, steps, map[string]session.Tool{"weather_forecast": tool}, specs)
	s := r.open(t, "teagan")
	wait(t, heard(s, "what's the weather like today"))

	if c := callByID(t, r.state(t, s.ConversationID()), "call_w1"); c.Outcome != "ok" {
		t.Errorf("call = %+v, want it run", c)
	}
	want := session.Caller{ConversationID: s.ConversationID(), CallID: "call_w1", Satellite: "kitchen"}
	if got := <-callers; got != want {
		t.Errorf("caller = %+v, want %+v: told nobody's identity", got, want)
	}
}

// Declared guest-scoped, remember has nobody to remember for, even with
// Teagan speaking: it keeps nothing, as it would for a guest (SPEC §5).
//
// verifies SPEC §5
func TestAGuestScopedRememberKeepsNothingForTheSpeaker(t *testing.T) {
	specs := maps.Clone(registry.Specs)
	guest := specs["remember"]
	guest.Scope, guest.UnknownSpeaker = registry.ScopeGuest, ""
	specs["remember"] = guest
	steps := []step{
		{act: session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"Takes oat milk in coffee."}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	store := memory.NewMemStore()
	r, _ := newMemoryRig(t, steps, memory.Tools(store, journal.FixedClock(epoch)), specs)
	s := r.open(t, "teagan")
	wait(t, heard(s, "remember that I take oat milk"))

	if c := callByID(t, r.state(t, s.ConversationID()), "call_r1"); c.Outcome != "error" {
		t.Errorf("call = %+v, want it refused for want of a person", c)
	}
	if got, err := store.Recall(context.Background(), "teagan", memory.RecallLimit); err != nil || len(got) != 0 {
		t.Errorf("Teagan remembers %+v (err %v), want nothing kept", got, err)
	}
}

// A person-scoped tool that lets a guest fall back runs for them, as nobody.
//
// verifies SPEC §5
func TestAGuestFallbackToolRunsForAGuest(t *testing.T) {
	specs := maps.Clone(registry.Specs)
	specs["calendar_today"] = registry.ToolSpec{
		Name: "calendar_today", OnInterrupt: registry.InterruptCancel,
		Scope: registry.ScopePerson, UnknownSpeaker: "guest_fallback",
		Timeout: registry.Specs["remember"].Timeout,
	}
	steps := []step{
		{act: session.ToolCall{ID: "call_c1", Tool: "calendar_today", Args: `{}`}},
		{act: session.TurnEnd{FinishReason: "tool_calls", Completion: "{}"}},
	}
	callers := make(chan session.Caller, 1)
	tool := session.ToolFunc(func(ctx context.Context, _ string) (string, error) {
		c, _ := session.CallerFrom(ctx)
		callers <- c
		return `{"events":["bins out"]}`, nil
	})
	r, _ := newMemoryRig(t, steps, map[string]session.Tool{"calendar_today": tool}, specs)
	s := r.open(t, "")
	wait(t, heard(s, "what's on today"))

	if c := callByID(t, r.state(t, s.ConversationID()), "call_c1"); c.Outcome != "ok" {
		t.Errorf("call = %+v, want it run for the guest", c)
	}
	if got := <-callers; got.Person != "" {
		t.Errorf("caller = %+v, want nobody", got)
	}
}
