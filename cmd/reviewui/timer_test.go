package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// Twelve minutes after Alan set it, the oven goes off in an empty kitchen.
// Browse lists it as an announcement nobody woke the kitchen for, named by
// what it said and whom it was for; the house log itself is no conversation.
//
// verifies SPEC §4, §9.2
func TestBrowseListsTheOvenGoingOffAsAnAnnouncement(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, "/conversations?day=2025-10-09")
	for _, want := range []string{
		`href="/conversations/` + convOven + `"`,
		">announcement<",
		"The oven timer is done.",
		"no wake word",
		"for alan · kitchen",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("Browse is missing %q", want)
		}
	}
	if strings.Contains(h, journal.HouseTimers) {
		t.Error("the house log is listed as a conversation")
	}
	if strings.Contains(get(t, s, "/replays"), convOven) {
		t.Error("an announcement nobody answered is offered for replay")
	}
}

// The oven's log says why the kitchen spoke unasked, plays what it said, and
// carries the house log's word that the timer was heard. It has no turn, so
// nothing to replay.
//
// verifies SPEC §4, §9.2
func TestTheOvenGoingOffSaysWhyItWasSaid(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, conversationHref(convOven))
	for _, want := range []string{
		"opened on kitchen to announce",
		"no wake word",
		"timer " + household.OvenTimer + " went off",
		`id="house-2"`,
		"went off: announced",
		"closed: announced",
		"blob%3A%2F%2Ftts%2Foven-done",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the oven's log is missing %q", want)
		}
	}
	if strings.Contains(h, "Replay ›") {
		t.Error("an announcement with no turn offers a replay")
	}
}

// The conversation Alan set the timer in shows the house log's timer_started
// just after the timer_start call that made it, not at the end of the log.
//
// verifies SPEC §9.2
func TestTheTimerIsShownSetInTheConversationThatSetIt(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, conversationHref(convTimer))
	call, set, result := strings.Index(h, `id="seq-8"`), strings.Index(h, `id="house-1"`), strings.Index(h, `id="seq-9"`)
	if call < 0 || set < 0 || result < 0 || call >= set || set >= result {
		t.Fatalf("timer_start at %d, timer_started at %d, its result at %d: want them in that order", call, set, result)
	}
	for _, want := range []string{"set the oven timer for 12m0s on kitchen", "goes off 2025-10-09T12:22:07.1Z", "house #1"} {
		if !strings.Contains(h, want) {
			t.Errorf("the timer's conversation is missing %q", want)
		}
	}
	// The house log's own page is not doubled up.
	if own := get(t, s, conversationHref(journal.HouseTimers)); strings.Contains(own, `id="house-`) {
		t.Error("the house log's own page repeats its events as house rows")
	}
}

// Teagan sets a rice timer in the office that evening, and the office
// satellite is unplugged when it goes off. Triage queues the timer nobody
// heard as a failure, and its row opens the house log at the event.
//
// verifies SPEC §7, §9.2
func TestATimerNobodyHeardIsQueuedAsAFailure(t *testing.T) {
	store := householdJournal(t)
	clk := &stepClock{now: household.Day().Add(19*time.Hour + 30*time.Minute)}
	j := journal.New(store, clk, household.Versions())
	fires := clk.now.Add(20 * time.Minute)
	for _, step := range []struct {
		after time.Duration
		rec   journal.Record
	}{
		{0, journal.Record{Kind: journal.KindTimerStarted, Fields: map[string]string{
			"timer_id": "t_7f3a9b21", "seconds": "1200", "fires_at": fires.Format(time.RFC3339Nano), "label": "rice",
			"satellite": "office", "person": "teagan", "conversation_id": "conv-1930-office", "call_id": "c1",
		}}},
		{25 * time.Minute, journal.Record{Kind: journal.KindTimerFinished, Fields: map[string]string{
			"timer_id": "t_7f3a9b21", "outcome": "unannounced", "error": "office: not connected",
		}}},
	} {
		clk.now = clk.now.Add(step.after)
		if _, err := j.Append(context.Background(), journal.HouseTimers, step.rec); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(store, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)

	q := get(t, s, "/queue?tab=failure")
	if !strings.Contains(q, `href="/conversations/house:timers#seq-4"`) || !strings.Contains(q, "unannounced") {
		t.Errorf("the failure queue does not open the rice timer nobody heard:\n%s", q)
	}
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/conversations/house:timers", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "went off: unannounced") || !strings.Contains(w.Body.String(), "office: not connected") {
		t.Errorf("the house log = %d, want the rice timer and why nobody heard it", w.Code)
	}
}
