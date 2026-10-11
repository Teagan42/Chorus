package timer_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/timer"
)

// reads records which part of each log a start asked the store for.
type reads struct {
	*journal.MemStore
	mu    sync.Mutex
	whole []string
	after []uint64
}

func (r *reads) Events(ctx context.Context, id string) ([]journal.Event, error) {
	r.mu.Lock()
	r.whole = append(r.whole, id)
	r.mu.Unlock()
	return r.MemStore.Events(ctx, id)
}

func (r *reads) EventsAfter(ctx context.Context, id string, seq uint64) ([]journal.Event, error) {
	r.mu.Lock()
	r.after = append(r.after, seq)
	r.mu.Unlock()
	return r.MemStore.EventsAfter(ctx, id, seq)
}

func replayFrom(t *testing.T, e journal.Event) uint64 {
	t.Helper()
	n, err := strconv.ParseUint(e.Fields["replay_from"], 10, 64)
	if err != nil {
		t.Fatalf("%s #%d replay_from %q: %v", e.Kind, e.Seq, e.Fields["replay_from"], err)
	}
	return n
}

// Each timer event says where a start may replay from: the earliest timer
// still running once it is written, or past it when nothing is.
//
// verifies SPEC §8
func TestEveryTimerEventSaysWhereAStartReplaysFrom(t *testing.T) {
	h := newHouse("kitchen")
	s, _ := h.start(t)
	ctx := context.Background()
	oven, err := s.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 60, Label: "oven"})
	if err != nil {
		t.Fatal(err)
	}
	pasta, err := s.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 600, Label: "pasta"})
	if err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)
	h.finished(t, 1)
	if _, err := s.Cancel(ctx, alanInTheKitchen, pasta.ID); err != nil {
		t.Fatal(err)
	}

	log := h.log(t)
	var got []uint64
	for _, e := range log {
		got = append(got, replayFrom(t, e))
	}
	// oven at #1, pasta at #2; the oven ends at #3 leaving pasta from #2;
	// pasta's cancel at #4 leaves nothing, so a start reads from #5.
	if want := []uint64{1, 1, 2, 5}; !slices.Equal(got, want) {
		t.Errorf("replay_from = %v, want %v (oven %s, pasta %s)", got, want, oven.ID, pasta.ID)
	}
}

// A week of the household's timers have all gone off; tonight's lasagne is
// still in. The restart reads the house log from the lasagne's start, not
// the week before it, and the lasagne still goes off at its time.
//
// verifies SPEC §8
func TestAStartReplaysTheHouseLogFromTheOldestRunningTimer(t *testing.T) {
	store := &reads{MemStore: journal.NewMemStore()}
	h := &house{store: store, clock: newClock(), ann: newAnnouncer("kitchen")}
	ctx := context.Background()
	first, stop := h.start(t)
	for i := range 7 {
		if _, err := first.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 60, Label: fmt.Sprintf("tea %d", i)}); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(time.Minute)
		h.finished(t, i+1)
	}
	lasagne, err := first.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 2400, Label: "lasagne"})
	if err != nil {
		t.Fatal(err)
	}
	stop()
	lasagneAt := h.log(t)[len(h.log(t))-1].Seq

	store.mu.Lock()
	store.whole, store.after = nil, nil
	store.mu.Unlock()
	second, _ := h.start(t)

	store.mu.Lock()
	whole, after := slices.Clone(store.whole), slices.Clone(store.after)
	store.mu.Unlock()
	if slices.Contains(whole, journal.HouseTimers) {
		t.Errorf("the start read the whole house log")
	}
	if want := []uint64{lasagneAt - 1, lasagneAt - 1}; !slices.Equal(after, want) {
		t.Errorf("read after %v, want the last event then the lasagne on (%v)", after, want)
	}
	full, err := journal.Replay(ctx, store.MemStore, journal.HouseTimers, journal.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Running(); !slices.Equal(got, full.Running()) || len(got) != 1 || got[0].ID != lasagne.ID {
		t.Errorf("running = %+v, want the full replay's %+v", got, full.Running())
	}

	await(t, "the lasagne to be armed again", func() bool { return h.clock.armed() > 0 })
	h.clock.advance(40 * time.Minute)
	if done := h.finished(t, 8); done.Fields["timer_id"] != lasagne.ID || done.Fields["outcome"] != "announced" {
		t.Errorf("timer_finished = %v, want the lasagne announced", done.Fields)
	}
}

// With nothing running the start reads only the last event, and the next
// timer still takes the next seq.
//
// verifies SPEC §8
func TestAStartWithNothingRunningReadsOneEvent(t *testing.T) {
	h := newHouse("kitchen")
	ctx := context.Background()
	first, stop := h.start(t)
	if _, err := first.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 60, Label: "tea"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)
	h.finished(t, 1)
	stop()

	second, _ := h.start(t)
	if got := second.Running(); len(got) != 0 {
		t.Fatalf("running = %+v, want none", got)
	}
	if _, err := second.Set(ctx, alanInTheKitchen, timer.Request{Seconds: 60, Label: "eggs"}); err != nil {
		t.Fatalf("set after restart: %v", err)
	}
	log := h.log(t)
	if last := log[len(log)-1]; last.Seq != 3 || replayFrom(t, last) != 3 {
		t.Errorf("eggs at #%d from %s, want #3 replaying from 3", last.Seq, last.Fields["replay_from"])
	}
}

// A house log written before replay_from replays whole, as it always did.
//
// verifies SPEC §8
func TestAHouseLogFromBeforeReplayFromReplaysWhole(t *testing.T) {
	h := newHouse("kitchen")
	j := journal.New(h.store, h.clock, journal.Versions{})
	fires := supper.Add(12 * time.Minute).Format(time.RFC3339Nano)
	for _, f := range []map[string]string{
		{"timer_id": "t_tea", "seconds": "60", "fires_at": supper.Add(-time.Hour).Format(time.RFC3339Nano), "satellite": "kitchen", "conversation_id": "conv-1", "call_id": "c1"},
		{"timer_id": "t_oven", "seconds": "720", "fires_at": fires, "satellite": "kitchen", "conversation_id": "conv-1", "call_id": "c2"},
	} {
		if _, err := j.Append(context.Background(), journal.HouseTimers, journal.Record{Kind: journal.KindTimerStarted, Fields: f}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.Append(context.Background(), journal.HouseTimers, journal.Record{Kind: journal.KindTimerFinished, Fields: map[string]string{
		"timer_id": "t_tea", "outcome": "announced", "conversation_id": "conv-2",
	}}); err != nil {
		t.Fatal(err)
	}
	s, _ := h.start(t)
	if got := s.Running(); len(got) != 1 || got[0].ID != "t_oven" {
		t.Errorf("running = %+v, want the oven", got)
	}
}

// Retention deleting the week's ended timers leaves the start rebuilding
// exactly what it did before.
//
// verifies SPEC §8
func TestAStartAfterEndedTimersAreDeletedRebuildsTheSame(t *testing.T) {
	h := newHouse("kitchen")
	ctx := context.Background()
	first, stop := h.start(t)
	teagan := session.Caller{Person: "teagan", ConversationID: "conv-1", CallID: "c1", Satellite: "kitchen"}
	if _, err := first.Set(ctx, teagan, timer.Request{Seconds: 60, Label: "tea"}); err != nil {
		t.Fatal(err)
	}
	h.clock.advance(time.Minute)
	h.finished(t, 1)
	if _, err := first.Set(ctx, teagan, timer.Request{Seconds: 1800, Label: "bread"}); err != nil {
		t.Fatal(err)
	}
	stop()
	before := first.Running()
	if err := h.store.(journal.Deleter).DeleteEvents(ctx, journal.HouseTimers, []uint64{1, 2}); err != nil {
		t.Fatal(err)
	}
	second, _ := h.start(t)
	if got := second.Running(); !slices.Equal(got, before) {
		t.Errorf("running = %+v, want %+v", got, before)
	}
}
