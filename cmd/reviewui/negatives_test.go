package main

import (
	"context"
	"encoding/json"
	"fmt"
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

// withLostWake adds the wake the office lost to the kitchen at breakfast:
// Teagan said it at the kitchen counter, and the office heard it fainter.
func withLostWake(t *testing.T, store *journal.MemStore) *journal.MemStore {
	t.Helper()
	j := journal.New(store, journal.FixedClock(household.Day().Add(7*time.Hour+42*time.Minute)), household.Versions())
	r := journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/office-lost", Fields: map[string]string{
		"reason": "arbitrated", "lost_to": "kitchen", "energy_dbfs": "-31.2", "winner_energy_dbfs": "-18.4",
	}}
	if _, err := j.Append(context.Background(), "device:office", r); err != nil {
		t.Fatalf("append lost wake: %v", err)
	}
	return store
}

// The office's log shows the wake it lost and to whom, with no verdict to
// give: it was the wake word, answered in the kitchen, so it never ships as
// a hard negative and Browse draws no rejected wake for it (SPEC §4.5, §9.3).
//
// verifies SPEC §4.5, §9.3
func TestAWakeLostToAnotherSatelliteIsShownAndNeverShips(t *testing.T) {
	s := newServer(withLostWake(t, householdJournal(t)), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)

	if got := ids(corpus(t, s)); strings.Join(got, " ") != "device:kitchen/7 device:office/1" {
		t.Errorf("corpus = %v, want the dishwasher and the podcast alone", got)
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Download 2 negatives (JSONL)") {
		t.Error("the export page counts the lost wake as a negative")
	}

	h := get(t, s, "/conversations/device:office")
	i := strings.Index(h, `id="seq-2"`)
	if i < 0 {
		t.Fatal("the office's log does not show the lost wake")
	}
	for _, want := range []string{"wake lost to kitchen", "-31.2 dBFS against -18.4", "office-lost"} {
		if !strings.Contains(h[i:], want) {
			t.Errorf("the lost wake's row is missing %q", want)
		}
	}
	if strings.Contains(h, `id="wake-2"`) {
		t.Error("the lost wake offers a verdict, as though it were a negative")
	}
	if !strings.Contains(h, `id="wake-1"`) {
		t.Error("the podcast lost its verdict")
	}
	if code, _ := post(t, s, "/conversations/device:office/wakes/2/confirmed", nil); code != http.StatusNotFound {
		t.Errorf("a verdict on the lost wake = %d, want 404", code)
	}

	if h := get(t, s, "/conversations"); strings.Contains(h, "wake rejected at 07:42") {
		t.Error("Browse draws the lost wake as a rejected one")
	}
}

// Bob presses the office Voice PE's button and thinks better of it. The
// office's log shows the rejected press and why it has no verdict: the
// wake model never fired, so it never ships as a hard negative (SPEC §9.3).
//
// verifies SPEC §9.3
func TestAButtonPressIsShownAndNeverShips(t *testing.T) {
	store := householdJournal(t)
	j := journal.New(store, journal.FixedClock(household.Day().Add(21*time.Hour+40*time.Minute)), household.Versions())
	r := journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/office-press", Fields: map[string]string{
		"reason": "no_speech", "wake_word": "button",
	}}
	if _, err := j.Append(context.Background(), "device:office", r); err != nil {
		t.Fatalf("append press: %v", err)
	}
	s := newServer(store, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)

	if got := ids(corpus(t, s)); strings.Join(got, " ") != "device:kitchen/7 device:office/1" {
		t.Errorf("corpus = %v, want the dishwasher and the podcast alone", got)
	}
	h := get(t, s, "/conversations/device:office")
	i := strings.Index(h, `id="seq-2"`)
	if i < 0 {
		t.Fatal("the office's log does not show the press")
	}
	for _, want := range []string{"wake rejected: no_speech", "a button press: the wake model never fired"} {
		if !strings.Contains(h[i:], want) {
			t.Errorf("the press's row is missing %q", want)
		}
	}
	if strings.Contains(h, `id="wake-2"`) {
		t.Error("the press offers a verdict, as though it were a negative")
	}
}

// Bob presses the office Voice PE's button and asks for the porch light.
// The conversation's first row says the session was opened by a press, so
// a reviewer can tell it from a spoken wake (SPEC §9.3, ADR-0072).
//
// verifies SPEC §9.3
func TestASessionOpenedByAPressSaysSo(t *testing.T) {
	store := householdJournal(t)
	j := journal.New(store, journal.FixedClock(household.Day().Add(21*time.Hour+45*time.Minute)), household.Versions())
	r := journal.Record{Kind: journal.KindSessionOpened, Fields: map[string]string{
		"satellite": "office", "speaker_id": "bob", "resumed": "false", "wake_word": "button",
	}}
	if _, err := j.Append(context.Background(), "conv_porch", r); err != nil {
		t.Fatalf("append: %v", err)
	}
	s := newServer(store, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)

	h := get(t, s, "/conversations/conv_porch")
	i := strings.Index(h, `id="seq-1"`)
	if i < 0 {
		t.Fatal("the conversation does not show its opening")
	}
	if !strings.Contains(h[i:], "opened by the button") {
		t.Errorf("the opening row does not say it was a press:\n%s", h[i:min(len(h), i+600)])
	}
	if h := get(t, s, "/conversations/"+convWeather); strings.Contains(h, "opened by the button") {
		t.Error("a spoken wake's opening says it was a press")
	}
}

// Alan's "hey eddie" meets an STT box that is restarting. The kitchen's log
// shows the rejected wake and why it has no verdict: nothing could decode
// it, so nothing judged the audio, and it never ships as a hard negative
// (SPEC §9.3, ADR-0060).
//
// verifies SPEC §9.3
func TestAWakeNothingCouldDecodeIsShownAndNeverShips(t *testing.T) {
	store := householdJournal(t)
	j := journal.New(store, journal.FixedClock(household.Day().Add(18*time.Hour+5*time.Minute)), household.Versions())
	r := journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/kitchen-oven", Fields: map[string]string{
		"reason": "transcription_failed", "wake_word": "hey_eddie", "canned_call_id": "cn_0a1b2c3d",
	}}
	ev, err := j.Append(context.Background(), "device:kitchen", r)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	seq := ev.Seq
	s := newServer(store, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)

	if got := ids(corpus(t, s)); strings.Join(got, " ") != "device:kitchen/7 device:office/1" {
		t.Errorf("corpus = %v, want the dishwasher and the podcast alone", got)
	}
	h := get(t, s, "/conversations/device:kitchen")
	i := strings.Index(h, fmt.Sprintf(`id="seq-%d"`, seq))
	if i < 0 {
		t.Fatal("the kitchen's log does not show the undecoded wake")
	}
	for _, want := range []string{"wake rejected: transcription_failed", "nothing could decode it"} {
		if !strings.Contains(h[i:], want) {
			t.Errorf("the undecoded wake's row is missing %q", want)
		}
	}
	if strings.Contains(h, fmt.Sprintf(`id="wake-%d"`, seq)) {
		t.Error("the undecoded wake offers a verdict, as though it were a negative")
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
