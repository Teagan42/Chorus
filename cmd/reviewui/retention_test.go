package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// monthOn is the household's Thursday thirty days later, when the kitchen's
// audio horizon has passed.
var monthOn = time.Date(2025, time.November, 8, 3, 0, 0, 0, time.UTC)

const zeppelinPlaying = "blob://tts/zeppelin-playing"

// prunedHousehold is the day with the clip of "Playing Led Zeppelin one."
// removed by retention a month on, as chorusd's pruner leaves it.
func prunedHousehold(t *testing.T) (*server, *journal.MemStore) {
	t.Helper()
	ctx := context.Background()
	store, blobs := householdJournal(t), householdBlobs(t)
	if err := blobs.Remove(ctx, zeppelinPlaying); err != nil {
		t.Fatal(err)
	}
	j := journal.New(store, journal.FixedClock(monthOn), journal.Versions{})
	if _, err := j.Append(ctx, convZeppel, journal.Record{Kind: journal.KindAudioDropped, Fields: map[string]string{
		"audio_ref": zeppelinPlaying, "reason": "retention", "days": "30",
	}}); err != nil {
		t.Fatal(err)
	}
	return newServer(store, curation.NewMemStore(), blobs, household.ReviewedAt), store
}

// A clip retention removed shows as pruned where it was, with no player
// that would only fail; every other clip of the conversation still plays,
// and the pruning itself is a line of the log.
//
// verifies SPEC §8, §9.2
func TestAPrunedClipShowsAsPrunedNotAsABrokenPlayer(t *testing.T) {
	s, _ := prunedHousehold(t)
	h := get(t, s, conversationHref(convZeppel))
	if strings.Contains(h, "/audio?ref="+url.QueryEscape(zeppelinPlaying)) {
		t.Error("the page still offers a player for the pruned clip")
	}
	for _, want := range []string{
		"audio pruned",
		"audio pruned after 30 days",
		"/audio?ref=" + url.QueryEscape("blob://tts/zeppelin-list"),
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the conversation page is missing %q", want)
		}
	}
}

// Pruning a month later does not stretch the conversation to a month long,
// or change anything else Browse draws of the day.
//
// verifies SPEC §8
func TestPruningLeavesBrowseAsItWas(t *testing.T) {
	before, _ := newHouseholdServer(t)
	after, _ := prunedHousehold(t)
	const day = "/conversations?day=2025-10-09"
	if a, b := get(t, after, day), get(t, before, day); a != b {
		t.Errorf("Browse changed when a clip was pruned a month later")
	}
}

// A log retention deleted leaves nothing of itself in what the review UI
// keeps between requests (ADR-0062).
//
// verifies SPEC §8
func TestADeletedLogIsForgottenByTheReviewUI(t *testing.T) {
	store := householdJournal(t)
	s := newServer(store, curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)
	get(t, s, "/conversations?day=2025-10-09")
	if _, ok := s.logs.byID[convZeppel]; !ok {
		t.Fatalf("the zeppelin conversation was never kept")
	}
	if err := store.DeleteLog(context.Background(), convZeppel); err != nil {
		t.Fatal(err)
	}
	h := get(t, s, "/conversations?day=2025-10-09")
	if _, ok := s.logs.byID[convZeppel]; ok {
		t.Error("the deleted conversation is still kept")
	}
	if strings.Contains(h, conversationHref(convZeppel)) {
		t.Error("Browse still lists the deleted conversation")
	}
}

// rowOf is the markup of one event's row on a conversation page, up to the
// next row.
func rowOf(page string, seq int) string {
	anchor := fmt.Sprintf(`id="seq-%d"`, seq)
	i := strings.Index(page, anchor)
	if i < 0 {
		return ""
	}
	row := page[i:]
	if j := strings.Index(row[len(anchor):], `id="seq-`); j >= 0 {
		row = row[:len(anchor)+j]
	}
	return row
}

// The row that named a pruned clip says so itself, where its player was,
// not only the audio_dropped line at the bottom of the log.
//
// verifies SPEC §8, §9.2
func TestAPrunedClipsOwnRowSaysItIsGone(t *testing.T) {
	s, store := prunedHousehold(t)
	events, err := store.Events(context.Background(), convZeppel)
	if err != nil {
		t.Fatal(err)
	}
	seq := 0
	for _, e := range events {
		if e.AudioRef == zeppelinPlaying && e.Kind == journal.KindSpeechSpoken {
			seq = int(e.Seq)
		}
	}
	row := rowOf(get(t, s, conversationHref(convZeppel)), seq)
	if !strings.Contains(row, "Playing Led Zeppelin one.") || !strings.Contains(row, "audio pruned") {
		t.Errorf("the pruned clip's row #%d does not say it is gone:\n%s", seq, row)
	}
}
