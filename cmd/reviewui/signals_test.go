package main

import (
	"context"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/reviewui/household"
	"github.com/teagan42/chorus/internal/triage"
)

// The household's day has ten turns nobody corrected: every completed turn
// but the two cut, the garage failure and the oven ask Alan repeated.
//
// verifies SPEC §9.1
func TestTheHouseholdsWeakPositivesAreTheTurnsNobodyCorrected(t *testing.T) {
	store := householdJournal(t)
	var n int
	for id := range household.Logs() {
		pos, err := triage.WeakPositives(context.Background(), store, id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		for _, p := range pos {
			n++
			if id == convTimer && p.Utterance == "set a timer for the oven" {
				t.Error("the oven ask Alan had to repeat is a weak positive")
			}
			if id == convGarage {
				t.Error("the garage turn whose sensor timed out is a weak positive")
			}
		}
	}
	if n != 10 {
		t.Errorf("%d weak positives, want 10", n)
	}
}

// Weak positives are their own pile on Triage, apart from the problems
// "All" lists, and each opens its turn's completion with the tag beside it.
//
// verifies SPEC §9.1, §9.2
func TestTriageKeepsTheWeakPositivesInTheirOwnPile(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, "/queue?tab=weak-positive")
	for _, want := range []string{
		`href="/queue?tab=weak-positive">Weak positives <span class="tabs__count">10</span>`,
		`href="/queue?tab=all">All <span class="tabs__count">6</span>`,
		"add oat milk to the shopping list",
		"answered “Added oat milk.” · not cut off, asked again or failed",
		`href="/conversations/` + convList + `#seq-8"`,
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the weak positives tab is missing %q", want)
		}
	}
	if strings.Contains(h, "is the garage door closed") {
		t.Error("the weak positives tab lists the garage failure")
	}
	if h := get(t, s, "/queue"); strings.Contains(h, "add oat milk to the shopping list") {
		t.Error("All lists a weak positive among the problems")
	}
	h = get(t, s, conversationHref(convList))
	row := h[strings.Index(h, `id="seq-8"`):]
	if !strings.Contains(row[:strings.Index(row, "</div>")], "weak positive") {
		t.Error("the shopping list's completion does not say it is a weak positive")
	}
}
