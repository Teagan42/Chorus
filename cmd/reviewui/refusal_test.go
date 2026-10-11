package main

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// brokenStore is the curation table with the database gone from under it:
// every write fails with what pgx would say, every read still answers.
type brokenStore struct {
	curation.Store

	mu  sync.Mutex
	err error
}

func (b *brokenStore) fail() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.err
}

// mend brings the database back.
func (b *brokenStore) mend() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = nil
}

func (b *brokenStore) Put(ctx context.Context, d curation.Decision) error {
	if err := b.fail(); err != nil {
		return err
	}
	return b.Store.Put(ctx, d)
}

func (b *brokenStore) PutAnnotation(ctx context.Context, a curation.Annotation) error {
	if err := b.fail(); err != nil {
		return err
	}
	return b.Store.PutAnnotation(ctx, a)
}

func (b *brokenStore) PutWakeVerdict(ctx context.Context, v curation.WakeVerdict) error {
	if err := b.fail(); err != nil {
		return err
	}
	return b.Store.PutWakeVerdict(ctx, v)
}

func (b *brokenStore) PutPromotion(ctx context.Context, p curation.Promotion) error {
	if err := b.fail(); err != nil {
		return err
	}
	return b.Store.PutPromotion(ctx, p)
}

// errDatabaseGone is what pgx says once Postgres has gone away mid-evening.
var errDatabaseGone = errors.New("pgx: failed to connect to `host=localhost user=chorus database=chorus`: dial error (connection refused)")

// newBrokenHouseholdServer serves the day over a curation table that
// refuses every write.
func newBrokenHouseholdServer(t *testing.T) (*server, *brokenStore) {
	t.Helper()
	broken := &brokenStore{Store: curation.NewMemStore(), err: errDatabaseGone}
	return newServer(householdJournal(t), broken, householdBlobs(t), household.ReviewedAt), broken
}

// A post the server refuses comes back as a sentence the page can show,
// never a bare "404 page not found": htmx drops the body of an error
// unless the page puts it in front of the reviewer, and the page does.
//
// verifies SPEC §9.2
func TestARefusedPostSaysWhyInASentence(t *testing.T) {
	s, _ := newHouseholdServer(t)
	for _, c := range []struct {
		target string
		status int
		says   string
	}{
		// The pair is gone from the list the page was drawn from.
		{"/pairs/conv-0000-attic/3/accept", http.StatusNotFound, `No pair "conv-0000-attic/3" is in the journal`},
		{"/pairs/" + pairZeppel + "/dance", http.StatusBadRequest, "The pair cannot take that"},
		// Seq 1 opened the session; it is no turn.
		{"/conversations/" + convZeppel + "/turns/1/labels/too_slow", http.StatusNotFound, "Turn #1 of " + convZeppel + " is not an utterance, so it takes no label"},
		{"/conversations/" + convZeppel + "/turns/2/labels/made_up", http.StatusNotFound, `"made_up" is not a label this UI knows`},
		{"/conversations/" + convZeppel + "/turns/first/labels/too_slow", http.StatusNotFound, `"first" is not a turn number`},
		{"/conversations/device:attic/wakes/7/confirmed", http.StatusNotFound, "No rejected wake #7 is in device:attic"},
		{"/conversations/" + convZeppel + "/wakes/7/confirmed", http.StatusNotFound, "names no rejected wake on a satellite's log"},
		{"/replays/" + convZeppel + "/runs/9/turns/2/promote", http.StatusNotFound, "No kept re-run 9 of " + convZeppel},
		{"/replays/" + convZeppel, http.StatusServiceUnavailable, "No model is configured, so nothing can be re-run"},
	} {
		code, body := post(t, s, c.target, url.Values{})
		if code != c.status {
			t.Errorf("POST %s = %d, want %d", c.target, code, c.status)
		}
		if !strings.Contains(body, c.says) {
			t.Errorf("POST %s says %q, want %q", c.target, strings.TrimSpace(body), c.says)
		}
		if strings.Contains(body, "page not found") {
			t.Errorf("POST %s is a bare refusal: %q", c.target, strings.TrimSpace(body))
		}
	}
}

// The database goes away under the reviewer. Each write says so and names
// what was not stored, with the driver's words after it.
//
// verifies SPEC §9.2
func TestAWriteTheStoreRefusesSaysWhatWasLost(t *testing.T) {
	s, broken := newBrokenHouseholdServer(t)
	for _, c := range []struct{ target, says string }{
		{"/pairs/" + pairZeppel + "/accept-anyway", "The verdict was not stored"},
		{"/conversations/" + convZeppel + "/turns/2/labels/too_slow", "The labels were not stored"},
		{dishwasherWake + "confirmed", "The verdict on the wake was not stored"},
	} {
		code, body := post(t, s, c.target, url.Values{})
		if code != http.StatusInternalServerError {
			t.Errorf("POST %s = %d, want 500", c.target, code)
		}
		if !strings.Contains(body, c.says) || !strings.Contains(body, "connection refused") {
			t.Errorf("POST %s says %q, want %q and the driver's words", c.target, strings.TrimSpace(body), c.says)
		}
	}
	// Postgres back, the same press lands.
	broken.mend()
	if code, body := post(t, s, "/pairs/"+pairZeppel+"/accept-anyway", url.Values{}); code != http.StatusOK {
		t.Errorf("POST accept-anyway once the database is back = %d: %s", code, body)
	}
}
