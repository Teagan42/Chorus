package journal_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Audio going away a month later is no fact about the conversation: the
// reducer folds the kitchen's closed conversation to the same state with or
// without it, so a replay of a pruned log is the replay it always was.
//
// verifies SPEC §8
func TestReduceFoldsDroppedAudioToNoChange(t *testing.T) {
	at := time.Date(2026, 9, 1, 7, 42, 0, 0, time.UTC)
	events := []journal.Event{
		{Seq: 1, Kind: journal.KindSessionOpened, At: at, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}},
		{Seq: 2, Kind: journal.KindUtteranceTranscribed, At: at, AudioRef: "blob://mic/u1", Fields: map[string]string{"text": "what's the weather"}},
		{Seq: 3, Kind: journal.KindSessionClosed, At: at, Fields: map[string]string{"reason": "model_ended", "satellite": "kitchen"}},
	}
	before := foldAll(t, events)
	after := foldAll(t, append(slices.Clone(events), journal.Event{
		Seq: 4, Kind: journal.KindAudioDropped, At: at.AddDate(0, 0, 30),
		Fields: map[string]string{"audio_ref": "blob://mic/u1", "reason": "retention", "days": "30"},
	}))
	after.LastSeq = before.LastSeq
	if !reflect.DeepEqual(after, before) {
		t.Errorf("dropped audio changed the state\n got: %+v\nwant: %+v", after, before)
	}
}

// A store with no tail read still answers EventsAfter, by reading the whole
// log and dropping its head.
//
// verifies SPEC §8
func TestEventsAfterFallsBackToTheWholeLog(t *testing.T) {
	ctx := context.Background()
	s := journal.NewMemStore()
	appendN(t, s, journal.HouseTimers, 4)

	got, err := journal.EventsAfter(ctx, wholeOnly{s}, journal.HouseTimers, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seqs(got), []uint64{3, 4}) {
		t.Errorf("after 2 = %v, want [3 4]", seqs(got))
	}
}

// wholeOnly hides MemStore's tail read.
type wholeOnly struct{ s *journal.MemStore }

func (w wholeOnly) Append(ctx context.Context, e journal.Event) error { return w.s.Append(ctx, e) }
func (w wholeOnly) Events(ctx context.Context, id string) ([]journal.Event, error) {
	return w.s.Events(ctx, id)
}

func (w wholeOnly) LastSeq(ctx context.Context, id string) (uint64, error) {
	return w.s.LastSeq(ctx, id)
}

func foldAll(t *testing.T, events []journal.Event) journal.State {
	t.Helper()
	var (
		s   journal.State
		err error
	)
	for _, e := range events {
		if s, err = journal.Reduce(s, e); err != nil {
			t.Fatalf("reduce %s: %v", e.Kind, err)
		}
	}
	return s
}
