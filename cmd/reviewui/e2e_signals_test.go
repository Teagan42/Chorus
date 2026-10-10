//go:build e2e

// Journeys through the signals the log holds besides barge-ins: the wakes
// a satellite rejected, the ask Alan had to repeat, and the turns nobody
// corrected.
package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/curation"
)

// wakeButton is a rejected wake's verdict control by the verdict it posts.
func wakeButton(seq int, status string) string {
	return fmt.Sprintf(`#wake-%d button[hx-post$="/%s"]`, seq, status)
}

// wakeCorpus fetches the wake corpus as the Export page's link would.
func (p *page) wakeCorpus() []map[string]any {
	p.t.Helper()
	var rows []map[string]any
	p.eval(`fetch("/export/wake-negatives.jsonl").then(r => r.text()).then(t => t.trim().split("\n").filter(Boolean).map(l => JSON.parse(l)))`, &rows)
	return rows
}

// waitHash waits for the fragment to become want: a same-page link moves
// the fragment without loading anything.
func (p *page) waitHash(want string) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`location.hash === %q`, want)
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("the fragment never became %s: %v", want, err)
	}
}

// Afternoon: the kitchen woke on the dishwasher, the office on a podcast.
// The reviewer opens the dishwasher from its tick on Browse, hears both
// channels, and confirms it; opens the office's log from its lane's name
// and keeps the podcast out. Export's wake corpus ships the dishwasher
// alone, confirmed, apart from the DPO dataset.
//
// verifies SPEC §9.3
func TestE2EJourneyARejectedWakeIsHeardJudgedAndShipped(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/conversations")
	p.follow(`.day-lanes__reject[href="/conversations/device:kitchen#seq-7"]`)
	if got := p.path(); got != "/conversations/device:kitchen" {
		t.Errorf("the dishwasher's tick landed on %s", got)
	}
	if id, visible := p.target(); id != "seq-7" || !visible {
		t.Errorf("target #%s (visible %v), want the rejection #seq-7 in view", id, visible)
	}
	closeTo(t, "the dishwasher on both channels", p.durations("#seq-7"), []float64{0.8, 0.8})
	p.waitText("#wake-7", "Hard negative: ships with the wake corpus unless you discard it.")
	p.click(wakeButton(7, "confirmed"))
	p.pressed(wakeButton(7, "confirmed"), true)
	p.waitText("#wake-7", "Confirmed: ships with the wake corpus.")
	p.shot("journey-wake-confirmed")

	p.visit("/conversations")
	p.follow(`a.day-lanes__head[href="/conversations/device:office"]`)
	p.waitText("#seq-1", "wake rejected: no_speech")
	p.click(wakeButton(1, "discarded"))
	p.pressed(wakeButton(1, "discarded"), true)
	p.waitText("#wake-1", "Kept out of the wake corpus.")

	// A reload keeps both: the verdicts are stored, not the page's state.
	p.visit("/conversations/device:kitchen")
	p.pressed(wakeButton(7, "confirmed"), true)
	p.pressed(wakeButton(7, "discarded"), false)
	vs, err := decisions.WakeVerdicts(context.Background(), "device:office")
	if err != nil || vs[1].Status != curation.WakeDiscarded {
		t.Errorf("the office's stored verdicts = %+v (%v)", vs, err)
	}

	p.visit("/export")
	wake := `section[aria-label="Wake-word hard negatives"]`
	p.waitText(wake, "Download 1 negative (JSONL)")
	want := map[string]string{"Ships": "1", "Confirmed": "1", "Held · unknown speaker": "0", "Discarded": "1"}
	if got := p.metrics(wake); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("wake corpus piles = %v, want %v", got, want)
	}
	p.shot("journey-wake-export")
	rows := p.wakeCorpus()
	if len(rows) != 1 || rows[0]["id"] != "device:kitchen/7" || rows[0]["confirmed"] != true ||
		rows[0]["second_audio"] != "blob://wake/kitchen-dishwasher-second" {
		t.Errorf("the wake corpus is %v, want the confirmed dishwasher on both channels", rows)
	}
	if n := len(p.download()); n != 0 {
		t.Errorf("the DPO dataset has %d rows; a wake verdict is no pair", n)
	}
}

// Lunchtime: Alan asked for the oven timer twice. From the repeat on
// Triage the reviewer reaches the answer that failed, labels it with what
// it should have asked, and accepts the pair in Curate. The exported row
// carries the repeat as its evidence.
//
// verifies SPEC §9.1, §9.2
func TestE2EJourneyLabelTheAnswerAlanHadToRepeat(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/queue?tab=repeated")
	p.follow(`#queue a.list__row`)
	p.waitText("#seq-8", "asked again 6.2 s after")
	p.click(`#seq-8 a[href="#turn-2-labels"]`)
	p.waitHash("#turn-2-labels")
	p.waitText("#turn-2-labels", "asked again at #8: “set a timer for twelve minutes”")
	p.click(chip(ovenAsk, "misunderstood_intent"))
	p.pressed(chip(ovenAsk, "misunderstood_intent"), true)
	p.typeInto("#turn-2-should-have", fixOven)
	p.click(`#turn-2-labels button[type="submit"]`)
	p.waitText("#turn-2-labels", "In Curate")
	p.waitText("#turn-2-labels", "asked again at #8")
	p.shot("journey-repeat-labelled")
	if a, _ := decisions.Annotations(context.Background(), convTimer); a[ovenAsk].ShouldHave != fixOven {
		t.Errorf("stored annotation = %+v", a[ovenAsk])
	}

	p.follow("#turn-2-labels a")
	p.waitText(".pair-actions", "asked again: “set a timer for twelve minutes”")
	p.waitText(".pair-actions", fixOven)
	p.shot("journey-repeat-curate")
	p.press("accept")
	p.waitText(".pair-actions", "Accepted")

	p.visit("/export")
	rows := p.download()
	if len(rows) != 1 {
		t.Fatalf("the dataset has %d rows, want the oven pair", len(rows))
	}
	meta := rows[0]["meta"].(map[string]any)
	seq := meta["seq"].(map[string]any)
	audio := meta["audio"].(map[string]any)
	if !strings.Contains(fmt.Sprint(meta["heard"]), "asked again: “set a timer for twelve minutes”") ||
		seq["correction"] != float64(8) || audio["correction"] != "blob://mic/timer-twelve" {
		t.Errorf("the oven row's evidence = heard %q, seq %v, audio %v", meta["heard"], seq, audio)
	}
}

// Evening: the reviewer looks for something that went right. Triage keeps
// the turns nobody corrected in a pile of their own; the shopping list
// opens at its completion, and labelled an exemplar it stays a positive,
// making no pair.
//
// verifies SPEC §9.1, §9.2
func TestE2EJourneyAWeakPositiveIsLabelledAnExemplar(t *testing.T) {
	s, decisions := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/queue")
	if strings.Contains(p.text("#queue"), "add oat milk") {
		t.Error("All lists a weak positive among the problems")
	}
	p.follow(`#queue-tabs a[href$="tab=weak-positive"]`)
	if n := p.count("#queue a.list__row"); n != 12 {
		t.Errorf("the weak positives tab lists %d rows, want 12", n)
	}
	p.shot("triage-weak-positives")

	p.follow(fmt.Sprintf(`#queue a[href="%s#seq-9"]`, conversationHref(convList)))
	if id, visible := p.target(); id != "seq-9" || !visible {
		t.Errorf("target #%s (visible %v), want the completion #seq-9 in view", id, visible)
	}
	p.waitText("#seq-9", "weak positive")
	p.waitText("#seq-9", "answered “Added oat milk.”")
	p.click(chip(2, "exemplar"))
	p.pressed(chip(2, "exemplar"), true)
	if a, _ := decisions.Annotations(context.Background(), convList); !a[2].Has(curation.LabelExemplar) {
		t.Errorf("stored annotation = %+v", a[2])
	}
	p.visit("/curate/pairs")
	if strings.Contains(p.text("#pair-rows"), "Added oat milk.") {
		t.Error("an exemplar made a pair")
	}

	empty := open(t, emptyServer())
	empty.visit("/queue?tab=weak-positive")
	empty.waitText("#queue", "Nothing in this pile.")
	empty.shot("triage-weak-positives-empty")
}
