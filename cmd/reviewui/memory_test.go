package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

func forgetFactHref(m memory.Memory) string {
	return memoryRoute + "/" + m.Person + "/facts/" + m.ID + "/forget"
}

func forgetSummaryHref(s memory.Summary) string {
	return memoryRoute + "/" + s.Person + "/summaries/" + s.ConversationID + "/forget"
}

// The memory screen lists what the assistant remembers per person: each
// fact with whose it is, whether the household shares it, when it was
// kept and where, and each conversation summary it is told; each with its
// own Forget behind a second press. Browse links to it.
//
// verifies SPEC §5
func TestTheMemoryScreenListsWhatItRemembersPerPerson(t *testing.T) {
	v := newVisit(t)
	if !strings.Contains(get(t, v.s, "/conversations"), `href="`+memoryRoute+`"`) {
		t.Error("Browse does not link to the memory screen")
	}
	page := get(t, v.s, memoryRoute)
	for _, want := range []string{
		`aria-label="About alan"`, `aria-label="About alice"`, `aria-label="About teagan"`,
		"Allergic to peanuts.", "shared with the household", "kept Thu 9 Oct 09:31",
		`<a href="` + conversationHref(household.ConvGarage) + `">` + household.ConvGarage + `</a>`,
		"Book club meets here on Thursdays at seven.", "conv-week-before-teagan · no longer in the journal",
		"Alan&#39;s sister asked where a pharmacy was open; Mill Road, until ten.", "private to alan",
		`action="` + forgetFactHref(alicesAllergy) + `"`, `action="` + forgetSummaryHref(visitSummary) + `"`,
		`<input type="hidden" name="confirm" value="forget">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the memory screen is missing %q", want)
		}
	}
	if a, t2 := strings.Index(page, "About alan"), strings.Index(page, "About teagan"); a < 0 || t2 < a {
		t.Error("people are not listed in order")
	}
}

// Alice asks for her allergy to be forgotten. The reviewer forgets it
// through the memory store's own Forget: she is no longer recalled it, the
// screen swaps without it, Teagan's fact stays, and Deletions says Alice's
// memory went without saying what it was.
//
// verifies SPEC §5
func TestForgettingAlicesAllergy(t *testing.T) {
	v := newVisit(t)
	w := send(v.s.routes(), forgetFactHref(alicesAllergy), confirm("forget"))
	if w.Code != http.StatusOK {
		t.Fatalf("forget = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.HasPrefix(body, `<section id="memory"`) || strings.Contains(body, "peanuts") || !strings.Contains(body, "Book club") {
		t.Errorf("the swapped screen is wrong:\n%s", body)
	}
	recalled, err := v.memories.Recall(context.Background(), "alice", memory.RecallLimit)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range recalled {
		if m.ID == alicesAllergy.ID {
			t.Error("Alice is still recalled her allergy")
		}
	}
	if len(recalled) != 1 {
		t.Errorf("Alice recalls %v, want Teagan's shared book club only", recalled)
	}
	records, _ := v.s.erasures.List(context.Background())
	if len(records) != 1 || records[0].Kind != erasure.KindMemory || records[0].Person != "alice" || records[0].Subject != alicesAllergy.ID || records[0].ConversationID != household.ConvGarage {
		t.Errorf("records = %+v, want Alice's memory", records)
	}
	trail := get(t, v.s, deletionsRoute)
	if !strings.Contains(trail, "alice&#39;s "+alicesAllergy.ID) || strings.Contains(trail, "peanuts") {
		t.Error("Deletions does not name Alice's memory, or keeps what it was")
	}

	// Without htmx the form still lands, and comes back to the screen.
	plain, _ := post(t, v.s, forgetSummaryHref(visitSummary), confirm("forget"))
	if plain != http.StatusSeeOther {
		t.Errorf("a plain forget = %d, want 303", plain)
	}
	if ss, _ := v.memories.AllSummaries(context.Background()); len(ss) != 0 {
		t.Errorf("Alan's summary outlived its forget: %v", ss)
	}
}

// A forget is refused for a memory nobody has by that id, for someone
// else's memory (Teagan's fact is not Alice's to lose), for a summary not
// kept, without its second press, and with no memory store at all.
//
// verifies SPEC §5
func TestAForgetIsRefusedAndSaysWhy(t *testing.T) {
	v := newVisit(t)
	for _, c := range []struct {
		name, target string
		form         url.Values
		code         int
		says         string
	}{
		{"an unknown id", memoryRoute + "/alice/facts/m_00000000/forget", confirm("forget"), http.StatusNotFound, "nothing to forget"},
		{"someone else's memory", memoryRoute + "/alice/facts/" + teagansBookClub.ID + "/forget", confirm("forget"), http.StatusNotFound, "nothing to forget"},
		{"a summary not kept", memoryRoute + "/teagan/summaries/" + convGuest + "/forget", confirm("forget"), http.StatusNotFound, "nothing to forget"},
		{"no second press", forgetFactHref(alicesAllergy), nil, http.StatusBadRequest, "second press"},
	} {
		w := send(v.s.routes(), c.target, c.form)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.says) {
			t.Errorf("%s = %d %q, want %d saying %q", c.name, w.Code, w.Body.String(), c.code, c.says)
		}
	}
	if ms, _ := v.memories.AllMemories(context.Background()); len(ms) != 2 {
		t.Errorf("a refused forget forgot: %v", ms)
	}
	if records, _ := v.s.erasures.List(context.Background()); len(records) != 0 {
		t.Errorf("a refusal left a record: %+v", records)
	}

	bare := newServer(householdJournal(t), curation.NewMemStore(), householdBlobs(t), household.ReviewedAt)
	if page := get(t, bare, memoryRoute); !strings.Contains(page, "This review UI keeps no memories.") {
		t.Error("with no memory store the screen does not say so")
	}
	if w := send(bare.routes(), forgetFactHref(alicesAllergy), confirm("forget")); w.Code != http.StatusNotFound {
		t.Errorf("forget with no memory store = %d, want 404", w.Code)
	}
}

// Deletions starts empty, and says what lands there.
//
// verifies SPEC §8
func TestDeletionsStartsEmpty(t *testing.T) {
	s, _ := newHouseholdServer(t)
	page := get(t, s, deletionsRoute)
	if !strings.Contains(page, "Nothing has been deleted.") || !strings.Contains(page, `href="`+memoryRoute+`"`) {
		t.Error("an empty Deletions does not say so, or does not link to Memory")
	}
}
