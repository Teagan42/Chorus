package main

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// countingJournal counts each log's reads, so a test can say which logs a
// request went back to the store for.
type countingJournal struct {
	Journal
	mu    sync.Mutex
	reads map[string]int
}

func (c *countingJournal) Events(ctx context.Context, id string) ([]journal.Event, error) {
	c.mu.Lock()
	c.reads[id]++
	c.mu.Unlock()
	return c.Journal.Events(ctx, id)
}

func (c *countingJournal) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.reads)
}

func newCountingServer(t *testing.T) (*server, *countingJournal, *journal.MemStore) {
	t.Helper()
	store := householdJournal(t)
	c := &countingJournal{Journal: store, reads: map[string]int{}}
	return newServer(c, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt), c, store
}

// The screens that read the whole household.
var householdScreens = []string{"/conversations", "/queue", "/review", "/curate/pairs", "/export", "/replays"}

// Browse reads each log once, and the screens after it read none of them
// again: none has grown, so what they derive is what Browse derived.
//
// verifies SPEC §8
func TestAnUnchangedLogIsReadOnce(t *testing.T) {
	s, c, _ := newCountingServer(t)
	get(t, s, "/conversations")
	for id := range household.Logs() {
		if n := c.snapshot()[id]; n != 1 {
			t.Errorf("Browse read %s %d times, want once", id, n)
		}
	}
	before := c.snapshot()
	for range 2 {
		for _, screen := range householdScreens {
			get(t, s, screen)
		}
	}
	if after := c.snapshot(); !maps.Equal(before, after) {
		t.Errorf("unchanged logs were read again: %v, then %v", before, after)
	}
}

// resumeList is Teagan back at the office satellite after the shopping
// list: she cuts off the read-back and asks for the count instead. The
// conversation grows by one barge-in pair.
func resumeList(t *testing.T, store journal.Store) string {
	t.Helper()
	clk := &stepClock{}
	j := journal.New(store, clk, household.Versions())
	start := household.Day().Add(21*time.Hour + 6*time.Minute)
	var cut journal.Event
	for _, r := range []struct {
		sec float64
		rec journal.Record
	}{
		{0, journal.Record{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "teagan", "resumed": "true"}}},
		{0.3, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/list-whats-on", Fields: map[string]string{"text": "what's on the shopping list", "speaker_id": "teagan"}}},
		{0.4, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "ha_get_state", "call_id": "c2", "args_json": `{"entity_id":"todo.shopping_list"}`}}},
		{0.5, journal.Record{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c2", "outcome": "ok", "result_json": `{"state":"4"}`}}},
		{0.55, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s2", "args_json": `{"mode":"queue","streamed":true}`}}},
		{0.65, journal.Record{Kind: journal.KindSpeechStarted, Fields: map[string]string{"call_id": "s2", "wait_ms": "600"}}},
		{1.65, journal.Record{Kind: journal.KindBargeInDetected, AudioRef: "blob://mic/list-bargein", Fields: map[string]string{"tts_position_ms": "1000"}}},
		{1.75, journal.Record{Kind: journal.KindSpeechTruncated, AudioRef: "blob://tts/list-readback", Fields: map[string]string{
			"spoken_text": "Oat milk, eggs,", "unspoken_text": " bread and coffee.", "frames_played": "16000", "call_id": "s2", "reason": "barge_in",
		}}},
		{1.75, journal.Record{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s2", "outcome": "cancelled"}}},
		{1.85, journal.Record{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}}},
		{3.0, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/list-how-many", Fields: map[string]string{"text": "just how many", "speaker_id": "teagan"}}},
		{3.15, journal.Record{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "s3", "args_json": `{"mode":"queue","streamed":true}`}}},
		{3.25, journal.Record{Kind: journal.KindSpeechStarted, Fields: map[string]string{"call_id": "s3", "wait_ms": "500"}}},
		{4.25, journal.Record{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/list-four", Fields: map[string]string{"text": "Four things.", "frames_played": "16000", "call_id": "s3"}}},
		{4.35, journal.Record{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "s3", "outcome": "ok"}}},
		{4.45, journal.Record{Kind: journal.KindModelCompleted, Fields: map[string]string{"completion_json": "{}", "finish_reason": "stop"}}},
	} {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		e, err := j.Append(context.Background(), convList, r.rec)
		if err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
		if e.Kind == journal.KindSpeechTruncated {
			cut = e
		}
	}
	return fmt.Sprintf("%s/%d", convList, cut.Seq)
}

// The barge-in Teagan made after the last request is a pair on the next,
// and only the log it landed in is read again for it.
//
// verifies SPEC §8, §9.1
func TestAPairCutSinceTheLastRequestIsOnTheNext(t *testing.T) {
	s, c, store := newCountingServer(t)
	if h := get(t, s, "/curate/pairs"); strings.Count(h, `class="list__row`) != 3 {
		t.Fatalf("Curate lists %d pairs before the append, want the day's 3", strings.Count(h, `class="list__row`))
	}
	before := c.snapshot()
	id := resumeList(t, store)

	h := get(t, s, "/curate/pairs")
	if !strings.Contains(h, id) {
		t.Errorf("Curate does not list %s, cut since the last request", id)
	}
	after := c.snapshot()
	for log, n := range after {
		want := before[log]
		if log == convList {
			want++
		}
		if n != want {
			t.Errorf("%s read %d times after the append, want %d", log, n-before[log], want-before[log])
		}
	}
	if q := get(t, s, "/queue?tab=barge-in"); !strings.Contains(q, reviewHref(id)) {
		t.Errorf("Triage does not queue %s", id)
	}
}

// Reviewers reading while chorusd writes see whole logs and never an old
// one: run under -race, every screen answers, and once the writes stop each
// shows the last of them.
//
// verifies SPEC §8
func TestScreensReadWhileTheLogGrows(t *testing.T) {
	s, _, store := newCountingServer(t)
	routes := s.routes()
	var wg sync.WaitGroup
	codes := make(chan int, 8*len(householdScreens))
	for i := range 8 {
		wg.Go(func() {
			for j := range householdScreens {
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, httptest.NewRequest(http.MethodGet, householdScreens[(i+j)%len(householdScreens)], nil))
				codes <- w.Code
			}
		})
	}
	id := resumeList(t, store)
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != http.StatusOK {
			t.Errorf("a screen answered %d while the log grew", code)
		}
	}
	if h := get(t, s, "/curate/pairs"); !strings.Contains(h, id) {
		t.Errorf("Curate does not list %s once the writes stopped", id)
	}
}

// Replay's list goes back to the store only for the log that grew since
// Browse read the day: Teagan's resumed shopping list, which it then lists
// with the turns she added.
//
// verifies SPEC §8, §9.2
func TestTheReplayListReadsOnlyTheLogThatGrew(t *testing.T) {
	s, c, store := newCountingServer(t)
	get(t, s, "/conversations")
	before := c.snapshot()
	get(t, s, "/replays")
	if after := c.snapshot(); !maps.Equal(before, after) {
		t.Errorf("Replay read unchanged logs again: %v, then %v", before, after)
	}

	resumeList(t, store)
	h := get(t, s, "/replays")
	after := c.snapshot()
	for log, n := range after {
		want := before[log]
		if log == convList {
			want++
		}
		if n != want {
			t.Errorf("%s read %d times after the append, want %d", log, n-before[log], want-before[log])
		}
	}
	href := `href="` + replayHref(convList) + `"`
	at := strings.Index(h, href)
	if at < 0 {
		t.Fatalf("Replay does not list %s", convList)
	}
	if row, _, _ := strings.Cut(h[at:], "</a>"); !strings.Contains(row, "3 turns") {
		t.Errorf("the shopping list's row = %s, want its 3 turns", row)
	}
}
