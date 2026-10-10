//go:build e2e

// Journeys through what a reviewer adds to the log: labels and "what it
// should have done" on a turn, a re-run promoted to a pair, and the audit
// the door's confirmation leaves.
package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/reviewui/household"
)

// pressed waits for a chip to say it is on or off: the swap lands after the
// click returns.
func (p *page) pressed(sel string, on bool) {
	p.t.Helper()
	var ok bool
	js := fmt.Sprintf(`(() => { const e = document.querySelector(%q); return !!e && e.getAttribute("aria-pressed") === %q })()`, sel, fmt.Sprint(on))
	if err := chromedp.Run(p.ctx, chromedp.Poll(js, &ok, chromedp.WithPollingTimeout(5*time.Second))); err != nil {
		p.t.Fatalf("%s never turned aria-pressed=%v: %v", sel, on, err)
	}
}

// download fetches the dataset as the page's own Download link would.
func (p *page) download() []map[string]any {
	p.t.Helper()
	var rows []map[string]any
	p.eval(`fetch("/export/dpo.jsonl").then(r => r.text()).then(t => t.trim().split("\n").filter(Boolean).map(l => JSON.parse(l)))`, &rows)
	return rows
}

func chip(seq int, label string) string {
	return fmt.Sprintf(`#turn-%d-labels button[hx-post$="/labels/%s"]`, seq, label)
}

// Evening: book club came at seven. The reviewer opens the door's
// conversation from Browse and reads the whole audit in one place: what the
// turn was told Teagan remembers, the unlock held with its nonce, the "yes"
// it was redeemed after, and the summary written once it closed.
//
// verifies SPEC §5, §6
func TestE2EJourneyTheDoorWaitsForAYes(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/conversations?day=2025-10-09")
	p.waitText("#conversations", "unlock the front door")
	p.follow(fmt.Sprintf(`#conversations a[href="%s"]`, conversationHref(convDoor)))

	p.waitText("#seq-3", "told 2 memories and 1 earlier conversation")
	p.waitText("#seq-3", "Book club meets here on Thursdays at seven.")
	p.waitText("#seq-3", "Teagan locked the front door for the night.")
	p.waitText("#seq-5", "held ha_call_service for the person's yes")
	p.waitText("#seq-5", "nonce "+household.DoorNonce)
	p.waitText("#seq-14", "ha_call_service ran on a yes")
	p.waitText("#seq-14", `redeemed after #12, teagan: “yes”`)
	p.waitText("#seq-22", "Teagan let book club in")
	for _, seq := range []string{"3", "5", "14", "22"} {
		var tag string
		p.eval(fmt.Sprintf(`document.querySelector("#seq-%s .sig-tag").textContent`, seq), &tag)
		if tag == "" {
			t.Errorf("#seq-%s has no kind tag", seq)
		}
	}
	p.shot("journey-door-audit")
}

// The reviewer hears Alice's album list run on, labels the turn, and writes
// what it should have said. Curate holds it as a pair; accepted, it is the
// dataset's one row, with its labels.
//
// verifies SPEC §9.2
func TestE2EJourneyLabelATurnAndCurateWhatItShouldHaveDone(t *testing.T) {
	s, _ := newHouseholdServer(t)
	p := open(t, s)

	p.visit("/conversations/" + convZeppel)
	if n := p.count(`[id$="-labels"] .chip`); n != 8*3 {
		t.Errorf("%d label chips, want eight on each of the three turns", n)
	}
	p.click(chip(zeppelinAsk, "misunderstood_intent"))
	p.pressed(chip(zeppelinAsk, "misunderstood_intent"), true)
	if p.has("#turn-2-labels a") {
		t.Error("a label alone made a pair, with no chosen side")
	}
	// A label pressed mid-draft keeps the draft.
	p.typeInto("#turn-2-should-have", shouldHaveZeppel)
	p.click(chip(zeppelinAsk, "spoke_when_it_shouldnt"))
	p.pressed(chip(zeppelinAsk, "spoke_when_it_shouldnt"), true)
	var draft string
	p.eval(`document.querySelector("#turn-2-should-have").value`, &draft)
	if draft != shouldHaveZeppel {
		t.Errorf("pressing a label left the draft as %q", draft)
	}
	p.click(`#turn-2-labels button[type="submit"]`)
	p.waitText("#turn-2-labels", "In Curate")
	p.shot("journey-labels")

	// A reload keeps it: the labels are stored, not the page's state.
	p.visit("/conversations/" + convZeppel)
	p.pressed(chip(zeppelinAsk, "misunderstood_intent"), true)
	p.pressed(chip(zeppelinAsk, "too_slow"), false)
	var note string
	p.eval(`document.querySelector("#turn-2-should-have").value`, &note)
	if note != shouldHaveZeppel {
		t.Errorf("note after reload = %q", note)
	}

	p.follow("#turn-2-labels a")
	p.waitText(".pair-actions", "DPO pair · annotation")
	p.waitText(".pair-actions", `labelled “misunderstood intent”, “spoke when it shouldn't”`)
	p.waitText(".pair-actions", shouldHaveZeppel)
	if got := p.rowStatus(annotatedZeppel); got != "unreviewed" {
		t.Errorf("the new pair is %q in the list, want unreviewed", got)
	}
	p.shot("journey-curate-annotation")
	p.press("accept")
	p.waitText(".pair-actions", "Accepted")

	p.visit("/export")
	p.waitText(".page-head", "Download 1 row")
	rows := p.download()
	if len(rows) != 1 {
		t.Fatalf("the dataset has %d rows, want the labelled turn", len(rows))
	}
	meta := rows[0]["meta"].(map[string]any)
	if meta["source"] != "annotation" || fmt.Sprint(meta["labels"]) != "[misunderstood_intent spoke_when_it_shouldnt]" {
		t.Errorf("row meta = %v", meta)
	}
}

// The reviewer re-runs Teagan's forecast under a prompt that leads with the
// gist, likes what the cut turn would have said, and promotes it. It is
// accepted in Curate and ships, saying which prompt wrote it.
//
// verifies SPEC §9.2
func TestE2EJourneyPromoteTheBriefForecast(t *testing.T) {
	s, _ := householdReplayServer(t)
	p := open(t, s)

	p.visit("/replays/" + convWeather)
	if p.has("#replay-result button") {
		t.Error("a promotion is offered before anything was re-run")
	}
	p.editPrompt(strings.TrimSpace(briefPrompt))
	p.click(`form button[type="submit"]`)
	p.waitText("#turn-2", briefWeather)
	if n := p.count("#replay-result [id^=promote-] button"); n != 1 {
		t.Errorf("%d promotions offered, want the one turn that changed", n)
	}
	p.click("#promote-2 button")
	p.waitText("#promote-2", "promoted · qwen3-32b@1 · sys@edited")
	p.shot("journey-replay-promoted")

	p.follow("#promote-2 a")
	p.waitText(".pair-actions", "replay output · re-run under qwen3-32b@1 · sys@edited")
	if got := p.rowStatus(replayedWeather); got != "accepted" {
		t.Errorf("the promoted pair is %q in the list, want accepted", got)
	}

	p.visit("/export")
	p.waitText(".page-head", "Download 1 row")
	rows := p.download()
	if len(rows) != 1 {
		t.Fatalf("the dataset has %d rows, want the promoted turn", len(rows))
	}
	meta := rows[0]["meta"].(map[string]any)
	cv, _ := meta["chosen_versions"].(map[string]any)
	if meta["source"] != "replay" || cv["prompt"] != "sys@edited" {
		t.Errorf("row meta = %v", meta)
	}
}

// The reviewer re-runs the garage question under the brief prompt. It checks
// the contact sensor, which answers, and says nothing until it has. They
// promote that, and Curate and the dataset both carry the call alone.
//
// verifies SPEC §9.2
func TestE2EJourneyPromoteTheGarageSensorCheck(t *testing.T) {
	s, _ := householdReplayServer(t)
	p := open(t, s)

	p.visit("/replays/" + convGarage)
	p.editPrompt(strings.TrimSpace(briefPrompt))
	p.click(`form button[type="submit"]`)
	p.waitText("#turn-2", "binary_sensor.garage_door_contact")
	p.click("#promote-2 button")
	p.waitText("#promote-2", "promoted · qwen3-32b@1 · sys@edited")

	p.follow("#promote-2 a")
	p.waitText(".pair-actions", "binary_sensor.garage_door_contact")
	p.waitText(".pair-actions", "cover.garage_door")
	if got := p.rowStatus(replayedGarage); got != "accepted" {
		t.Errorf("the promoted pair is %q in the list, want accepted", got)
	}
	p.shot("journey-curate-calls-only")

	p.visit("/export")
	p.waitText(".page-head", "Download 1 row")
	rows := p.download()
	if len(rows) != 1 {
		t.Fatalf("the dataset has %d rows, want the promoted garage turn", len(rows))
	}
	chosen := rows[0]["chosen"].([]any)[0].(map[string]any)
	calls, _ := chosen["tool_calls"].([]any)
	if chosen["content"] != "" || len(calls) != 1 {
		t.Fatalf("chosen = %v", chosen)
	}
	fn := calls[0].(map[string]any)["function"].(map[string]any)
	if args := fn["arguments"].(map[string]any); fn["name"] != "ha_get_state" || args["entity_id"] != "binary_sensor.garage_door_contact" {
		t.Errorf("chosen call = %v", fn)
	}
}

// The labels wrap on a phone; neither the door's audit nor the re-run's
// promotion pushes the page sideways.
//
// verifies SPEC §9.2
func TestE2ELabelsAndPromotionsFitAPhone(t *testing.T) {
	s, _ := householdReplayServer(t)
	p := open(t, s)
	p.run(chromedp.EmulateViewport(390, 844, chromedp.EmulateMobile))
	p.visit("/conversations/" + convDoor)
	p.click(chip(doorYes, "exemplar"))
	p.pressed(chip(doorYes, "exemplar"), true)
	var overflow int
	p.eval(`document.documentElement.scrollWidth - window.innerWidth`, &overflow)
	if overflow > 0 {
		t.Errorf("the door's conversation scrolls %dpx sideways at phone width", overflow)
	}
	p.shot("phone-conversation-labels")
}
