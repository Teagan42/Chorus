package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// The day's rejected wakes, by device log and seq.
const (
	dishwasherWake = "/conversations/device:kitchen/wakes/7/"
	podcastWake    = "/conversations/device:office/wakes/1/"
	guestWake      = "/conversations/device:living_room/wakes/6/"
)

// negativeRow is the part of a wake corpus row these tests read.
type negativeRow struct {
	ID          string `json:"id"`
	Label       int    `json:"label"`
	Reason      string `json:"reason"`
	Satellite   string `json:"satellite"`
	Audio       string `json:"audio"`
	SecondAudio string `json:"second_audio"`
	Confirmed   bool   `json:"confirmed"`
}

// corpus downloads the wake corpus as the Export page links it.
func corpus(t *testing.T, s *server) []negativeRow {
	t.Helper()
	w := httptest.NewRecorder()
	s.routes().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/export/wake-negatives.jsonl", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("wake corpus = %d: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Content-Disposition"); got != `attachment; filename="chorus-wake-negatives.jsonl"` {
		t.Errorf("Content-Disposition = %q", got)
	}
	var rows []negativeRow
	for _, line := range strings.Split(strings.TrimSpace(w.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var r negativeRow
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("row %q: %v", line, err)
		}
		rows = append(rows, r)
	}
	return rows
}

func ids(rows []negativeRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// The dishwasher and the podcast ship as hard negatives with nobody's word
// (SPEC §9.3), in their own download beside the DPO dataset, not in it.
//
// verifies SPEC §9.3
func TestTheDaysRejectedWakesShipAsHardNegatives(t *testing.T) {
	s, _ := newHouseholdServer(t)
	rows := corpus(t, s)
	if strings.Join(ids(rows), " ") != "device:kitchen/7 device:office/1" {
		t.Fatalf("corpus = %v, want the dishwasher and the podcast", ids(rows))
	}
	dish := rows[0]
	if dish.Label != 0 || dish.Reason != "no_speech" || dish.Satellite != "kitchen" || dish.Confirmed ||
		dish.Audio != "blob://wake/kitchen-dishwasher" || dish.SecondAudio != "blob://wake/kitchen-dishwasher-second" {
		t.Errorf("dishwasher row = %+v", dish)
	}
	if strings.Contains(get(t, s, "/export/dpo.jsonl"), "wake") {
		t.Error("the DPO dataset carries a rejected wake")
	}
	h := get(t, s, "/export")
	for _, want := range []string{
		"Wake-word hard negatives",
		`href="/export/wake-negatives.jsonl"`,
		"Download 2 negatives (JSONL)",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the export page is missing %q", want)
		}
	}
}

// The reviewer hears the podcast and keeps it out, confirms the dishwasher,
// then changes their mind on the podcast: the corpus follows each word.
//
// verifies SPEC §9.3
func TestAReviewerConfirmsOrDiscardsARejectedWake(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	body := mustPost(t, s, podcastWake+"discarded", nil)
	if !strings.Contains(body, `id="wake-1"`) || !strings.Contains(body, "Kept out of the wake corpus.") {
		t.Errorf("the swapped verdict does not say the podcast is out:\n%s", body)
	}
	if got := ids(corpus(t, s)); strings.Join(got, " ") != "device:kitchen/7" {
		t.Errorf("after the discard the corpus is %v", got)
	}
	mustPost(t, s, dishwasherWake+"confirmed", nil)
	if rows := corpus(t, s); len(rows) != 1 || !rows[0].Confirmed {
		t.Errorf("the confirmed dishwasher ships as %+v", rows)
	}
	vs, err := decisions.WakeVerdicts(context.Background(), "device:kitchen")
	if err != nil || vs[7].Status != curation.WakeConfirmed || !vs[7].JudgedAt.Equal(household.ReviewedAt()) {
		t.Errorf("stored verdicts = %+v (%v)", vs, err)
	}
	// Pressed again, a verdict comes off.
	mustPost(t, s, podcastWake+"discarded", nil)
	if got := ids(corpus(t, s)); len(got) != 2 {
		t.Errorf("after taking the discard back the corpus is %v", got)
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Download 2 negatives (JSONL)") {
		t.Error("the export page does not count both negatives again")
	}
}

// withGuestWake adds a rejection only the voice check failed: someone the
// household has not enrolled said "hey eddie" in the living room.
func withGuestWake(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	j := journal.New(store, journal.FixedClock(household.Day().Add(20*time.Hour+30*time.Minute)), household.Versions())
	r := journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/living-room-guest", Fields: map[string]string{"reason": "unknown_speaker"}}
	if _, err := j.Append(context.Background(), "device:living_room", r); err != nil {
		t.Fatalf("append guest wake: %v", err)
	}
	return store
}

// A wake the model and the speech check both passed is probably the wake
// word from a guest; it waits for a reviewer's yes before it ships.
//
// verifies SPEC §9.3
func TestAGuestsRejectedWakeWaitsForAReviewer(t *testing.T) {
	s := newServer(withGuestWake(t, householdJournal(t)), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)
	if got := ids(corpus(t, s)); len(got) != 2 {
		t.Errorf("the guest's wake shipped unreviewed: %v", got)
	}
	if h := get(t, s, "/conversations/device:living_room"); !strings.Contains(h, "Held: only the voice check failed") {
		t.Error("the guest's wake does not say why it is held")
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Held · unknown speaker") {
		t.Error("the export page does not count the held wake")
	}
	mustPost(t, s, guestWake+"confirmed", nil)
	rows := corpus(t, s)
	if len(rows) != 3 || rows[1].ID != "device:living_room/6" || !rows[1].Confirmed {
		t.Errorf("after the reviewer's yes the corpus is %+v", rows)
	}
}

// A verdict names a rejected wake in a satellite's log, or nothing.
//
// verifies SPEC §9.3
func TestAWakeVerdictNamesARejectedWake(t *testing.T) {
	s, _ := newHouseholdServer(t)
	for _, target := range []string{
		"/conversations/device:kitchen/wakes/1/confirmed",      // presence, not a rejection
		"/conversations/device:kitchen/wakes/7/was_a_wake",     // not a verdict
		"/conversations/" + convWeather + "/wakes/2/confirmed", // a conversation's utterance
		"/conversations/device:kitchen/wakes/seven/confirmed",  // not a seq
		"/conversations/device:garage/wakes/1/discarded",       // no such satellite
	} {
		if code, _ := post(t, s, target, nil); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", target, code)
		}
	}
}
