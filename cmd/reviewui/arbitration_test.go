package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// inOrder reports whether every want appears in page, each after the last.
func inOrder(page string, want ...string) bool {
	for _, w := range want {
		i := strings.Index(page, w)
		if i < 0 {
			return false
		}
		page = page[i+len(w):]
	}
	return true
}

// tableOf is the part of page inside the element with id.
func tableOf(page, id string) string {
	i := strings.Index(page, `id="`+id+`"`)
	if i < 0 {
		return ""
	}
	page = page[i:]
	if j := strings.Index(page, "</table>"); j >= 0 {
		page = page[:j]
	}
	return page
}

// Thursday's six contests, read from the satellites' own logs: the living
// room lost four of the kitchen's wakes, all but one by more than 12 dB;
// the empty kitchen lost Alice's in the living room by 2.2 dB; the living
// room lost the office's by 20.7. The report counts each pair, says how
// their margins fall, lists the closest calls first, and links each to
// the lost wake in its satellite's log and the conversation the winner
// opened, so a reviewer can hear whether the loudest was the nearest.
//
// verifies SPEC §4.5
func TestTheArbitrationReportShowsEachPairsMargins(t *testing.T) {
	s, _ := newHouseholdServer(t)
	if browse := get(t, s, "/conversations"); !strings.Contains(browse, `href="`+arbitrationRoute+`"`) {
		t.Error("Browse does not link to the arbitration report")
	}
	page := get(t, s, arbitrationRoute)
	for _, want := range []string{"Contests", ">6<", "Median margin", ">12.5 dB<", "Within 3 dB", ">2<", "AGC"} {
		if !strings.Contains(page, want) {
			t.Errorf("the report is missing %q", want)
		}
	}

	pairs := tableOf(page, "arbitration-pairs")
	// Closest pair first: a small median is where to listen.
	if !inOrder(pairs, "kitchen → living_room", "living_room → kitchen", "living_room → office") {
		t.Errorf("the pairs are not closest first:\n%s", pairs)
	}
	for _, want := range []string{
		// living_room → kitchen: 1.6, 12.2, 12.8, 13.7.
		"<td class=\"\">living_room → kitchen</td><td class=\"mono\">4</td><td class=\"mono\">12.5 dB</td><td class=\"mono\">1</td><td class=\"mono muted\">0</td><td class=\"mono muted\">0</td><td class=\"mono\">3</td>",
		`<a href="/conversations/device:living_room#seq-7">1.6 dB · 18:57</a>`,
		"<td class=\"\">kitchen → living_room</td><td class=\"mono\">1</td><td class=\"mono\">2.2 dB</td>",
		`<a href="/conversations/device:kitchen#seq-10">2.2 dB · 18:39</a>`,
		"<td class=\"\">living_room → office</td><td class=\"mono\">1</td><td class=\"mono\">20.7 dB</td>",
	} {
		if !strings.Contains(pairs, want) {
			t.Errorf("the pairs table is missing %q", want)
		}
	}

	closest := tableOf(page, "arbitration-closest")
	if !inOrder(closest, "1.6 dB", "2.2 dB", "12.2 dB", "12.8 dB", "13.7 dB", "20.7 dB") {
		t.Errorf("the closest calls are not smallest margin first:\n%s", closest)
	}
	for _, want := range []string{
		"-31.0 against -29.4 dBFS",
		`<a href="/conversations/device:living_room#seq-7">device:living_room #7</a>`,
		`<a href="/conversations/` + household.ConvDoor + `">` + household.ConvDoor + `</a>`,
		`<a href="/conversations/` + household.ConvJazz + `">` + household.ConvJazz + `</a>`,
	} {
		if !strings.Contains(closest, want) {
			t.Errorf("the closest calls are missing %q", want)
		}
	}

	// The link lands on the row that already says who won.
	if row := rowOf(get(t, s, "/conversations/device:living_room"), 7); !strings.Contains(row, "wake lost to kitchen") || !strings.Contains(row, "-31.0 dBFS against -29.4") {
		t.Errorf("the closest call's row does not say it lost:\n%s", row)
	}
}

// A house where nothing was argued over says so, rather than an empty
// table; a contest whose levels were not recorded is counted but has no
// margin; and a winner no louder than the loser, as a button press beats a
// voice, sorts first and says why.
//
// verifies SPEC §4.5
func TestTheArbitrationReportWithoutContestsOrLevels(t *testing.T) {
	now := func() time.Time { return household.ReviewedAt() }
	empty := newServer(journal.NewMemStore(), curation.NewMemStore(), blob.NewMemory(), now)
	if page := get(t, empty, arbitrationRoute); !strings.Contains(page, "No wake was argued over.") {
		t.Error("an empty house does not say nothing was argued over")
	}

	store := journal.NewMemStore()
	j := journal.New(store, journal.FixedClock(household.Day().Add(19*time.Hour)), household.Versions())
	for id, r := range map[string]journal.Record{
		// The office's Voice PE pressed by Alan, louder in the kitchen.
		"device:kitchen": rec(journal.KindWakeRejected, "blob://wake/kitchen-press", "reason", "arbitrated", "lost_to", "office",
			"energy_dbfs", "-24.0", "winner_energy_dbfs", "-30.5"),
		// A log from a chorusd that did not record the levels.
		"device:living_room": rec(journal.KindWakeRejected, "blob://wake/living_room-old", "reason", "arbitrated", "lost_to", "kitchen"),
	} {
		if _, err := j.Append(context.Background(), id, r); err != nil {
			t.Fatal(err)
		}
	}
	s := newServer(store, curation.NewMemStore(), blob.NewMemory(), now)
	page := get(t, s, arbitrationRoute)
	if !strings.Contains(page, ">2<") {
		t.Error("the report does not count both contests")
	}
	pairs := tableOf(page, "arbitration-pairs")
	if !strings.Contains(pairs, "<td class=\"\">living_room → kitchen</td><td class=\"mono\">1</td><td class=\"mono muted\">—</td>") {
		t.Errorf("a contest with no levels is not counted without a margin:\n%s", pairs)
	}
	closest := tableOf(page, "arbitration-closest")
	if !strings.Contains(closest, "-6.5 dB") || !strings.Contains(closest, "no louder: a button press, or a tie the earlier wake took") {
		t.Errorf("the quieter winner does not say why it won:\n%s", closest)
	}
	if strings.Contains(closest, "device:living_room") {
		t.Error("a contest with no levels is listed among the closest calls")
	}
}
