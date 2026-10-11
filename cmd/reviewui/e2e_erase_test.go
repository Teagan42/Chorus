//go:build e2e

// Journeys through deleting and forgetting (ADR-0070): the guest's visit
// deleted, her ask's clip dropped, Alice's allergy forgotten, and a
// deletion the log refused at the last moment said on the page.
package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// landed polls js across the navigation an htmx redirect or refresh
// starts after the click returns: a poll the navigation cuts short is
// asked again on the new page, until five seconds have gone.
func (p *page) landed(js string) bool {
	p.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(500*time.Millisecond)))
		if err == nil && ok {
			return true
		}
	}
	return false
}

// waitPath waits until the address bar names want.
func (p *page) waitPath(want string) {
	p.t.Helper()
	if !p.landed(fmt.Sprintf(`location.pathname + location.search === %q && document.readyState === "complete"`, want)) {
		p.t.Fatalf("the page never moved to %s (now %s)", want, p.path())
	}
}

// waitReloaded waits until a reloaded page's sel says want.
func (p *page) waitReloaded(sel, want string) {
	p.t.Helper()
	js := fmt.Sprintf(`document.readyState === "complete" && (() => { const e = document.querySelector(%q); return !!e && e.innerText.toLowerCase().includes(%q) })()`, sel, strings.ToLower(want))
	if !p.landed(js) {
		p.t.Fatalf("%s never showed %q after the reload (now: %q)", sel, want, p.text(sel))
	}
}

// waitGone polls until nothing on the page says what.
func (p *page) waitGone(sel, what string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && !e.innerText.toLowerCase().includes(%q) })()`, sel, strings.ToLower(what))
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("%s still says %q: %v", sel, what, err)
	}
}

// Alan asks for his sister's visit to go. On its page the reviewer opens
// the panel at the bottom, reads what goes, and presses the second button;
// the browser lands on Thursday in Browse, which says the visit went and no
// longer lists it, and Deletions keeps the line.
//
// verifies SPEC §8
func TestE2EJourneyDeleteTheGuestsVisit(t *testing.T) {
	v := newVisit(t)
	p := open(t, v.s)
	p.visit(conversationHref(convGuest))
	p.click("#delete-conversation > summary")
	p.waitText("#delete-conversation", "Delete "+convGuest+" for good?")
	p.waitText("#delete-conversation", "cannot be restored")
	p.shot("journey-delete-confirm")

	p.click("#delete-conversation button[type=submit]")
	p.waitPath("/conversations?day=2025-10-09&deleted=" + url.QueryEscape(convGuest))
	p.waitText("body", "Deleted "+convGuest+" for good.")
	if p.has(fmt.Sprintf(`a[href="%s"]`, conversationHref(convGuest))) {
		t.Error("Browse still links to the deleted visit")
	}
	if got := len(v.log(t, convGuest)); got != 0 {
		t.Errorf("the visit keeps %d events", got)
	}
	p.shot("journey-delete-done")

	p.follow(`.alert a[href="/deletions"]`)
	p.waitText("#deletions", convGuest)
	p.waitText("#deletions", "8 events, 5 clips")
	p.waitGone("#deletions", "pharmacy")
}

// Alan keeps the visit but drops what his sister said. Her ask's row opens
// its own panel; the press reloads the page with the clip gone where its
// players were, and Alan's answer still plays.
//
// verifies SPEC §8, §9.2
func TestE2EJourneyDropTheGuestsAsk(t *testing.T) {
	v := newVisit(t)
	p := open(t, v.s)
	p.visit(conversationHref(convGuest))
	sel := fmt.Sprintf("#drop-%d", guestAskSeq)
	if n := p.count(fmt.Sprintf("#seq-%d audio", guestAskSeq)); n != 2 {
		t.Fatalf("her ask plays %d clips before the drop, want both channels", n)
	}
	p.click(sel + " > summary")
	p.waitText(sel, "Removes this clip and its second channel for good")
	p.click(sel + " button[type=submit]")
	p.waitReloaded(fmt.Sprintf("#seq-%d", guestAskSeq), "audio redacted")
	if n := p.count(fmt.Sprintf("#seq-%d audio", guestAskSeq)); n != 0 {
		t.Errorf("her ask still offers %d players", n)
	}
	closeTo(t, "the answer after the drop", p.durations("#seq-7"), []float64{0.5})
	p.waitText("body", "audio redacted")
	p.shot("journey-drop-audio")
	if v.kept(guestAsk) || v.kept(guestAskSecond) {
		t.Error("her ask is still kept")
	}
}

// Alice asks for her allergy to be forgotten. From Browse the reviewer
// opens the memory screen, finds it under Alice, shared with the household,
// and forgets it; the screen swaps without it, and Teagan's book club stays.
//
// verifies SPEC §5
func TestE2EJourneyForgetAlicesAllergy(t *testing.T) {
	v := newVisit(t)
	p := open(t, v.s)
	p.visit("/conversations")
	p.follow(`.page-head a[href="/memory"]`)
	p.waitText("#person-alice", "Allergic to peanuts.")
	p.waitText("#person-alice", "shared with the household")
	sel := "#forget-fact-" + alicesAllergy.ID
	p.click(sel + " > summary")
	p.waitText(sel, "Forget this about alice?")
	p.shot("journey-memory-forget")

	p.click(sel + " button[type=submit]")
	p.waitGone("#memory", "peanuts")
	p.waitText("#memory", "Book club meets here on Thursdays at seven.")
	recalled, err := v.memories.Recall(context.Background(), "alice", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range recalled {
		if m.ID == alicesAllergy.ID {
			t.Error("Alice is still recalled her allergy")
		}
	}
	p.shot("journey-memory")
}

// The page offered a deletion, and before the press landed Alan walked back
// into the living room and the visit resumed. The press is refused, the
// alert says why in the server's words, and nothing is gone.
//
// verifies SPEC §4.5, §8
func TestE2EARefusedDeletionSaysWhy(t *testing.T) {
	v := newVisit(t)
	p := open(t, v.s)
	p.visit(conversationHref(convGuest))
	p.expect(http.StatusConflict, deleteHref(convGuest))
	j := journal.New(v.store, journal.FixedClock(household.ReviewedAt().Add(-time.Minute)), household.Versions())
	if _, err := j.Append(context.Background(), convGuest, rec(journal.KindSessionOpened, "", "satellite", "living_room", "speaker_id", "alan", "resumed", "true")); err != nil {
		t.Fatal(err)
	}
	p.click("#delete-conversation > summary")
	p.click("#delete-conversation button[type=submit]")
	p.waitText("#alerts .alert", "did not go through.")
	p.waitText("#alerts .alert", "live session")
	p.waitText("#alerts .alert", "409 Conflict from POST "+deleteHref(convGuest))
	if got := len(v.log(t, convGuest)); got != 9 || !v.kept(guestAsk) {
		t.Errorf("a refused deletion changed the visit: %d events", got)
	}
}
