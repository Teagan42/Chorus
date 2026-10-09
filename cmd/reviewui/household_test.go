package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/reviewui/household"
	"github.com/teaganglenn/chorus/internal/triage"
)

// The household's Thursday (internal/reviewui/household): three people,
// three satellites, every signal Triage knows, and one conversation from the
// night before so Browse has a yesterday. The browser tests walk a reviewer
// through this day; TestHouseholdDay pins what it derives so a fixture edit
// cannot quietly hollow them out.

// The conversations and pairs, named the way the tests talk about them.
const (
	convWeather = household.ConvWeather
	convZeppel  = household.ConvZeppelin
	convGarage  = household.ConvGarage
	convTimer   = household.ConvTimer
	convJazz    = household.ConvJazz
	convList    = household.ConvList
	convLock    = household.ConvLock

	pairWeather = household.PairWeather
	pairZeppel  = household.PairZeppelin
	pairJazz    = household.PairJazz
)

// The chosen sides a reviewer writes once they have heard each cut.
const (
	fixWeather = "Cloudy this morning, rain from three and a high of fourteen, so take an umbrella."
	fixZeppel  = "I found three albums by Led Zeppelin. Playing the first, Led Zeppelin one."
)

func householdJournal(t *testing.T) *journal.MemStore {
	t.Helper()
	store, err := household.Journal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func householdBlobs(t *testing.T) *blob.Memory {
	t.Helper()
	m, err := household.Blobs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// newHouseholdServer serves the day with no verdicts yet.
func newHouseholdServer(t *testing.T) (*server, curation.Store) {
	t.Helper()
	decisions := curation.NewMemStore()
	s := newServer(householdJournal(t), decisions, householdBlobs(t), household.ReviewedAt)
	return s, decisions
}

// TestHouseholdDay pins what the browser tests count on finding in the day.
func TestHouseholdDay(t *testing.T) {
	store := householdJournal(t)
	ctx := context.Background()

	var pairs []string
	attributed := map[string]bool{}
	signals := map[triage.Kind][]string{}
	for id := range household.Logs() {
		if _, err := journal.Replay(ctx, store, id, journal.Overrides{}); err != nil {
			t.Errorf("%s does not replay: %v", id, err)
		}
		if id[:7] == "device:" {
			continue
		}
		hs, err := harvest.Harvest(ctx, store, id)
		if err != nil {
			t.Fatalf("harvest %s: %v", id, err)
		}
		for _, p := range hs {
			pairs = append(pairs, p.ID)
			attributed[p.ID] = p.Attributed
		}
		sigs, err := triage.Scan(ctx, store, id)
		if err != nil {
			t.Fatalf("triage %s: %v", id, err)
		}
		for _, s := range sigs {
			signals[s.Kind] = append(signals[s.Kind], s.ConversationID)
		}
	}
	if len(pairs) != 3 || !attributed[pairWeather] || !attributed[pairZeppel] || attributed[pairJazz] {
		t.Errorf("pairs = %v (attributed %v), want weather and zeppelin attributed, jazz not", pairs, attributed)
	}
	want := map[triage.Kind][]string{
		triage.KindBargeIn:     {convWeather, convZeppel, convJazz},
		triage.KindFailure:     {convGarage},
		triage.KindRepeated:    {convTimer},
		triage.KindSpeakerFlip: {convJazz},
	}
	for k, convs := range want {
		got := map[string]bool{}
		for _, c := range signals[k] {
			got[c] = true
		}
		if len(signals[k]) != len(convs) {
			t.Errorf("%s signals = %v, want %v", k, signals[k], convs)
		}
		for _, c := range convs {
			if !got[c] {
				t.Errorf("%s signals = %v, missing %s", k, signals[k], c)
			}
		}
	}
}

// The evening's curation through the handlers alone: the counterpart of the
// browser journey, so a failure there can be told apart from one here.
//
// verifies SPEC §9.1
func TestHouseholdCurationExportsOnlyTheFixedAttributedPairs(t *testing.T) {
	s, _ := newHouseholdServer(t)
	for _, step := range []struct {
		id, action string
		form       url.Values
	}{
		{pairWeather, "save", url.Values{"chosen": {fixWeather}}},
		{pairWeather, "accept", nil},
		{pairZeppel, "accept-anyway", nil},
		{pairZeppel, "save", url.Values{"chosen": {fixZeppel}}},
		{pairJazz, "save", url.Values{"chosen": {"I found three jazz playlists. Playing Late Night Jazz."}}},
		{pairJazz, "accept", nil},
	} {
		if code, h := post(t, s, "/pairs/"+step.id+"/"+step.action, step.form); code != http.StatusOK {
			t.Fatalf("%s %s = %d: %s", step.action, step.id, code, h)
		}
	}
	body := strings.TrimSpace(get(t, s, "/export/dpo.jsonl"))
	lines := strings.Split(body, "\n")
	if len(lines) != 2 {
		t.Fatalf("export has %d rows, want weather and zeppelin (jazz is unattributed):\n%s", len(lines), body)
	}
	for i, want := range []string{fixZeppel, fixWeather} {
		if !strings.Contains(lines[i], `"content":"`+want+`"`) {
			t.Errorf("row %d does not carry the fix %q:\n%s", i, want, lines[i])
		}
	}
	if h := get(t, s, "/export"); !strings.Contains(h, "Held · unattributed") || !strings.Contains(h, "Download 2 rows (JSONL)") {
		t.Error("the export page does not count the held jazz pair beside the two rows")
	}
}
