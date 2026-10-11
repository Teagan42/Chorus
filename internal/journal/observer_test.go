package journal_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// observed is an observer that keeps what it was told.
type observed struct {
	appended []journal.Event
	failed   []journal.Kind
	errs     []error
}

func (h *observed) Appended(e journal.Event) { h.appended = append(h.appended, e) }

func (h *observed) AppendFailed(k journal.Kind, err error) {
	h.failed = append(h.failed, k)
	h.errs = append(h.errs, err)
}

// refusingStore fails every write, as a Postgres that went away does.
type refusingStore struct{ journal.Store }

var errGone = errors.New("connection refused")

func (refusingStore) Append(context.Context, journal.Event) error { return errGone }

// The operational counters are derived from the log as it is written, so an
// observer hears each append exactly as stamped and stored, and each one that
// never reached the log (ADR-0068).
//
// verifies SPEC §8
func TestAnObserverHearsEachAppendAsStored(t *testing.T) {
	clk := journal.FixedClock(time.Date(2026, 10, 8, 7, 42, 0, 0, time.UTC))
	j := journal.New(journal.NewMemStore(), clk, versions())
	h := &observed{}
	j.Observe(h)

	e, err := j.Append(context.Background(), "conv-teagan", journal.Record{
		Kind:   journal.KindSessionOpened,
		Fields: map[string]string{"satellite": "kitchen", "speaker_id": "teagan"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(h.appended) != 1 || h.appended[0].Seq != e.Seq || h.appended[0].Kind != journal.KindSessionOpened {
		t.Fatalf("observer heard %+v, want the stored session_opened", h.appended)
	}
	if !h.appended[0].At.Equal(clk.Now()) || h.appended[0].Fields["satellite"] != "kitchen" {
		t.Errorf("observer heard %+v, want it stamped as stored", h.appended[0])
	}
	if len(h.failed) != 0 {
		t.Errorf("observer heard failures %v for a write that landed", h.failed)
	}
}

// A write the store refused, and one the taxonomy refused, both count as an
// append that failed: neither is in the log (SPEC §8).
//
// verifies SPEC §8
func TestAnObserverHearsAnAppendThatNeverLanded(t *testing.T) {
	h := &observed{}
	j := journal.New(refusingStore{journal.NewMemStore()}, journal.FixedClock(time.Unix(0, 0)), versions())
	j.Observe(h)

	_, err := j.Append(context.Background(), "conv-alice", journal.Record{
		Kind:   journal.KindToolResult,
		Fields: map[string]string{"call_id": "c1", "outcome": "ok"},
	})
	if !errors.Is(err, errGone) {
		t.Fatalf("append = %v, want the store's refusal", err)
	}
	if _, err := j.Append(context.Background(), "conv-alice", journal.Record{Kind: journal.KindSessionClosed}); err == nil {
		t.Fatal("a session_closed with no reason was accepted")
	}

	want := []journal.Kind{journal.KindToolResult, journal.KindSessionClosed}
	if len(h.failed) != 2 || h.failed[0] != want[0] || h.failed[1] != want[1] {
		t.Errorf("observer heard failures %v, want %v", h.failed, want)
	}
	if !errors.Is(h.errs[0], errGone) {
		t.Errorf("observer heard %v, want the store's error", h.errs[0])
	}
	if len(h.appended) != 0 {
		t.Errorf("observer heard %d appends that never landed", len(h.appended))
	}
}

// A journal nobody observes appends as it always has.
//
// verifies SPEC §8
func TestAnUnobservedJournalAppends(t *testing.T) {
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), versions())
	if _, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office"},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
}
