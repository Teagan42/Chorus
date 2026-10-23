package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	sess "github.com/teagan42/chorus/internal/session"
)

// row is the rendered log row of the conversation's first event of kind,
// cut from the page at its anchor.
func row(t *testing.T, s *server, conv string, kind journal.Kind) string {
	t.Helper()
	events, err := s.journal.Events(t.Context(), conv)
	if err != nil {
		t.Fatal(err)
	}
	h := get(t, s, conversationHref(conv))
	for i, e := range events {
		if e.Kind != kind {
			continue
		}
		start := strings.Index(h, `id="seq-`+strconv.FormatUint(e.Seq, 10)+`"`)
		if start < 0 {
			t.Fatalf("%s #%d is not on the page", kind, e.Seq)
		}
		r := h[start:]
		if i+1 < len(events) {
			r = r[:strings.Index(r, `id="seq-`+strconv.FormatUint(events[i+1].Seq, 10)+`"`)]
		}
		return r
	}
	t.Fatalf("%s has no %s", conv, kind)
	return ""
}

// Alan's "something -- quieter": the guess made at his breath shows in the
// log, said to be thrown away and why, and the guess at his last pause shows
// on the completion it became.
//
// verifies SPEC §9.2, §11
func TestTheLogShowsAGuessThrownAwayAndOneKept(t *testing.T) {
	s, _ := newHouseholdServer(t)
	guess := row(t, s, convJazz, journal.KindSpeculationDiscarded)
	for _, want := range []string{"speculation discarded", "guessed “something” in the pause", "discarded: they went on speaking", "the model ran 180 ms", "alan"} {
		if !strings.Contains(guess, want) {
			t.Errorf("the discarded guess's row is missing %q:\n%s", want, guess)
		}
	}

	events, err := s.journal.Events(t.Context(), convJazz)
	if err != nil {
		t.Fatal(err)
	}
	h := get(t, s, conversationHref(convJazz))
	last := events[len(events)-2] // the completion before the close
	if last.Kind != journal.KindModelCompleted {
		t.Fatalf("#%d is %s, want the last completion", last.Seq, last.Kind)
	}
	done := h[strings.Index(h, `id="seq-`+strconv.FormatUint(last.Seq, 10)+`"`):]
	if !strings.Contains(done[:strings.Index(done, `id="seq-`+strconv.FormatUint(last.Seq+1, 10)+`"`)], "asked in the pause, 640 ms before the turn was confirmed") {
		t.Error("the kept guess's completion does not say it was asked early")
	}
	if strings.Count(h, "asked in the pause") != 1 {
		t.Error("a completion asked after its turn claims an early ask")
	}
}

// The thrown-away guess is no turn: it opens no labels and is not replayed.
//
// verifies SPEC §9.2, §11
func TestAThrownAwayGuessIsNoTurn(t *testing.T) {
	s, _ := newHouseholdServer(t)
	events, err := s.journal.Events(t.Context(), convJazz)
	if err != nil {
		t.Fatal(err)
	}
	h := get(t, s, conversationHref(convJazz))
	for _, e := range events {
		if e.Kind == journal.KindSpeculationDiscarded && strings.Contains(h, `id="turn-`+strconv.FormatUint(e.Seq, 10)+`-labels"`) {
			t.Errorf("the discarded guess #%d is offered labels as if it were a turn", e.Seq)
		}
	}
	if r := get(t, s, replayHref(convJazz)); strings.Contains(r, "guessed") {
		t.Error("the replay offers the discarded guess")
	}
}

// Every reason a guess is thrown away reads as a reason, not a code.
//
// verifies SPEC §9.2
func TestEveryDiscardReasonIsSaid(t *testing.T) {
	for _, r := range []string{sess.DiscardResumed, sess.DiscardChanged, sess.DiscardNotATurn, sess.DiscardSessionClosed} {
		if got := speculatedBecause(r); got == r || got == "" {
			t.Errorf("speculatedBecause(%q) = %q", r, got)
		}
	}
}
