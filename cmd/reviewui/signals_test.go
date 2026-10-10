package main

import (
	"context"
	"net/url"
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

// Alan's oven ask, the twelve minutes he had to say again, and what the
// reviewer says the first answer should have been.
const (
	ovenAsk  = 2
	pairOven = convTimer + "/2/annotation"
	fixOven  = "How long should the oven timer run?"
)

// Alan asked for the oven timer twice. The repeat's row offers the first
// answer's labels, which say it was asked again; labelled and given what it
// should have said, that turn is a pair whose evidence is the repeat, heard
// and seen, through to its exported row.
//
// verifies SPEC §9.1, §9.2
func TestARepeatedAskIsTheEvidenceOnThePairItsFirstAnswerMakes(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := get(t, s, conversationHref(convTimer))
	repeat := h[strings.Index(h, `id="seq-7"`):]
	if !strings.Contains(repeat[:strings.Index(repeat, `id="turn-7-labels"`)], `href="#turn-2-labels"`) {
		t.Error("the repeat's row does not offer the first answer's labels")
	}
	first := h[strings.Index(h, `id="turn-2-labels"`):]
	if !strings.Contains(first[:strings.Index(first, `id="seq-3"`)], "asked again at #7: “set a timer for twelve minutes”") {
		t.Error("the first answer's labels do not say it was asked again")
	}

	mustPost(t, s, turnURL(convTimer, ovenAsk, "labels/misunderstood_intent"), nil)
	body := mustPost(t, s, turnURL(convTimer, ovenAsk, "note"), url.Values{"should_have": {fixOven}})
	if !strings.Contains(body, "asked again at #7") || !strings.Contains(body, "In Curate ›") {
		t.Errorf("the swapped labels lost the repeat or the pair:\n%s", body)
	}
	h = get(t, s, pairHref(pairOven, "all"))
	if !strings.Contains(h, "labelled “misunderstood intent” · asked again: “set a timer for twelve minutes”") {
		t.Error("Curate does not show the repeat as the pair's evidence")
	}

	mustPost(t, s, "/pairs/"+pairOven+"/accept", nil)
	line := exportRow(t, s, pairOven)
	for _, want := range []string{
		`"heard":"labelled “misunderstood intent” · asked again: “set a timer for twelve minutes”"`,
		`"correction":7`,
		`"correction":"blob://mic/timer-twelve"`,
		`"chosen":[{"role":"assistant","content":"` + fixOven + `"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the oven pair's row is missing %s:\n%s", want, line)
		}
	}
}
