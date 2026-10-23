package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// convLamp is Teagan's last ask of the night, in the office, half of it
// heard while the speaker-ID sidecar was down.
const convLamp = "conv-2231-office"

// outageNight is the household's Thursday with a speaker-ID outage at the
// end of it: Alan's "hey eddie" in the kitchen rejected because nothing
// could attribute it, and Teagan's follow-up in the office heard while the
// embedder failed, kept hers. It returns the server and the wake's seq.
func outageNight(t *testing.T) (*server, uint64) {
	t.Helper()
	store := householdJournal(t)
	clk := &tick{now: household.Day().Add(22*time.Hour + 31*time.Minute)}
	j := journal.New(store, clk, household.Versions())
	add := func(conv string, r journal.Record) journal.Event {
		t.Helper()
		clk.now = clk.now.Add(2 * time.Second)
		ev, err := j.Append(context.Background(), conv, r)
		if err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
		return ev
	}
	wake := add("device:kitchen", journal.Record{Kind: journal.KindWakeRejected, AudioRef: "blob://wake/kitchen-back-door", Fields: map[string]string{
		"reason": "speaker_id_failed", "wake_word": "hey_eddie",
	}})
	add(convLamp, journal.Record{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "teagan", "resumed": "false"}})
	add(convLamp, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://utt/office-lamp", Fields: map[string]string{
		"text": "turn off the office lamp", "speaker_id": "teagan", "speaker_match": "identified",
	}})
	add(convLamp, journal.Record{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://utt/office-desk-fan", Fields: map[string]string{
		"text": "and the desk fan", "speaker_match": "failed",
	}})
	add(convLamp, journal.Record{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended", "satellite": "office"}})
	// The clips are takes the household already has, kept under the outage's
	// own keys, so every row plays.
	blobs := householdBlobs(t)
	for key, from := range map[string]string{
		"wake/kitchen-back-door": "blob://mic/timer-oven",
		"utt/office-lamp":        "blob://mic/list-oatmilk",
		"utt/office-desk-fan":    "blob://mic/list-oatmilk",
	} {
		pcm, ok := blobs.Bytes(from)
		if !ok {
			t.Fatalf("the household has no %s", from)
		}
		w, err := blobs.Create(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(pcm); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	return newServer(store, curation.NewMemStore(), blobs, household.ReviewedAt), wake.Seq
}

type tick struct{ now time.Time }

func (c *tick) Now() time.Time { return c.now }

// The kitchen's log shows Alan's rejected wake and why it has no verdict:
// speaker ID failed, so nothing judged his voice, and it is neither held as
// a guest's nor shipped as a hard negative (SPEC §9.3, ADR-0097).
//
// verifies SPEC §9.3
func TestAWakeSpeakerIDFailedOnIsShownAndNeverShips(t *testing.T) {
	s, seq := outageNight(t)

	if got := ids(corpus(t, s)); strings.Join(got, " ") != "device:kitchen/7 device:office/1" {
		t.Errorf("corpus = %v, want the dishwasher and the podcast alone", got)
	}
	h := get(t, s, "/conversations/device:kitchen")
	i := strings.Index(h, fmt.Sprintf(`id="seq-%d"`, seq))
	if i < 0 {
		t.Fatal("the kitchen's log does not show the unattributed wake")
	}
	for _, want := range []string{"wake rejected: speaker_id_failed", "speaker ID failed, so nothing judged the voice"} {
		if !strings.Contains(h[i:], want) {
			t.Errorf("the unattributed wake's row is missing %q", want)
		}
	}
	if strings.Contains(h, fmt.Sprintf(`id="wake-%d"`, seq)) {
		t.Error("the unattributed wake offers a verdict, as though it were a negative")
	}
}

// Teagan's "and the desk fan" was heard while the embedder failed. Its row
// says so, and that the turn stayed hers; her identified ask before it says
// nothing of the kind (SPEC §5, ADR-0097).
//
// verifies SPEC §5
func TestAnUtteranceSpeakerIDFailedOnSaysTheTurnStayed(t *testing.T) {
	s, _ := outageNight(t)
	h := get(t, s, "/conversations/"+convLamp)
	fan := strings.Index(h, "and the desk fan")
	if fan < 0 {
		t.Fatal("the conversation does not show the desk fan")
	}
	if !strings.Contains(h[fan:], "speaker ID failed, so the turn stayed with whoever was speaking") {
		t.Errorf("the desk fan's row does not say speaker ID failed:\n%s", h[fan:min(len(h), fan+600)])
	}
	if n := strings.Count(h, "speaker ID failed"); n != 1 {
		t.Errorf("%d rows say speaker ID failed, want the desk fan alone", n)
	}
}
