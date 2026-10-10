package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// convCalendar is Alice asking the office about her calendar, in a log
// whose recall was written cut off mid-memory.
const convCalendar = "conv-1505-office"

// withUnreadableRecall adds Alice's calendar ask to the household's day, as
// a chorusd that died mid-write would have left it: a memories_json no
// reducer can decode.
func withUnreadableRecall(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	ctx := context.Background()
	start := household.Day().Add(15*time.Hour + 5*time.Minute)
	for i, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "alice", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/calendar", Fields: map[string]string{"text": "what's on my calendar tomorrow", "speaker_id": "alice"}},
		{Kind: journal.KindMemoryRecalled, Fields: map[string]string{"person": "alice", "memories_json": `[{"id":"m_9a2e7c14","person":"alice","fact":"Dentist on Fri`}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s1", "args_json": `{"mode":"queue","streamed":true}`}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/calendar", Fields: map[string]string{"text": "Just the dentist at ten.", "frames_played": "24000"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s1", "outcome": "ok"}},
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended", "satellite": "office"}},
	} {
		j := journal.New(store, journal.FixedClock(start.Add(time.Duration(i)*time.Second)), household.Versions())
		if _, err := j.Append(ctx, convCalendar, r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// One unreadable log costs the reviewer that conversation, not the day:
// every screen that reads the household still serves the rest of it, and
// says which log it left out.
//
// verifies SPEC §8, §9.2
func TestAnUnreadableLogIsSkippedAndNamed(t *testing.T) {
	s := newServer(withUnreadableRecall(t, householdJournal(t)), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)
	for _, c := range []struct{ path, rest string }{
		{"/conversations", "play something by zeppelin"},
		{"/queue", "play something by zeppelin"},
		{"/replays", "play something by zeppelin"},
		{"/curate/pairs", "I found three"},
		{"/review", "pair 1 of 3"},
		{"/export", "Exportable"},
	} {
		h := get(t, s, c.path)
		if !strings.Contains(h, "Skipped 1 log that would not read") || !strings.Contains(h, convCalendar) {
			t.Errorf("%s does not name the log it skipped", c.path)
		}
		if !strings.Contains(h, c.rest) {
			t.Errorf("%s lost the rest of the day: no %q", c.path, c.rest)
		}
	}
	// The day without the bad log says nothing about skipping.
	if h := get(t, newBrowseServer(t), "/conversations"); strings.Contains(h, "would not read") {
		t.Error("Browse names a skipped log when every log read")
	}
}

// With only the bad log in the journal, Review is empty, and says it left a
// log out rather than that nothing happened.
//
// verifies SPEC §8, §9.2
func TestAnEmptyReviewNamesTheLogItCouldNotRead(t *testing.T) {
	s := newServer(withUnreadableRecall(t, journal.NewMemStore()), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)
	h := get(t, s, "/review")
	if !strings.Contains(h, "Nothing to review.") || !strings.Contains(h, convCalendar) {
		t.Errorf("an empty Review does not name the log it skipped:\n%s", h)
	}
}
