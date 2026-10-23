//go:build e2e

package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// TestE2EEnrollingAliceByRecordingHerHere: the reviewer opens Voices from
// Browse, types Alice's id, records three takes on the browser's microphone
// and enrolls her. The page turned each take into the satellite's own format
// before sending it, or the server would have refused it; she lands in
// identities.yaml from three phrases and the screen says to restart chorusd.
//
// verifies SPEC §5
func TestE2EEnrollingAliceByRecordingHerHere(t *testing.T) {
	v := newVoicesVisit(t)
	v.emb.anyTake = []float32{1, 0, 0, 0}
	p := open(t, v.s)
	p.visit("/conversations")
	p.follow(`a[href="` + voicesRoute + `"]`)
	p.waitText("#voices", "Nobody is enrolled")
	p.waitText("[data-recorder]", "Or record them here")

	p.run(chromedp.SendKeys("#enroll-id", "alice", chromedp.ByQuery), chromedp.SendKeys("#enroll-name", "Alice", chromedp.ByQuery))
	for i := 1; i <= 3; i++ {
		p.click("[data-record]")
		p.waitText("[data-status]", "Recording")
		p.run(chromedp.Sleep(1300 * time.Millisecond))
		p.click("[data-record]")
		p.waitText("[data-takes]", "take-"+string(rune('0'+i))+".wav")
	}
	p.waitText("[data-status]", "3 takes ready")
	var files int
	p.eval(`document.querySelector("#enroll-takes").files.length`, &files)
	if files != 3 {
		t.Fatalf("the form carries %d takes, want the 3 recorded", files)
	}
	p.shot("voices-recorded")

	resp, err := chromedp.RunResponse(p.ctx, chromedp.Evaluate(`document.querySelector("#enroll button[type=submit]").click()`, nil))
	if err != nil || resp == nil {
		t.Fatalf("enroll navigated nowhere: %v", err)
	}
	if got := p.path(); got != voicesRoute+"?enrolled=alice" {
		t.Fatalf("enrolling landed on %s: %s", got, p.text("body"))
	}
	p.waitText("body", "Enrolled alice.")
	p.waitText("#voices", "voiceprint from 3 phrases")
	p.shot("voices-enrolled")

	if got := v.household(t).People; len(got) != 1 || got[0].ID != "alice" || got[0].Utterances != 3 {
		t.Fatalf("identities.yaml holds %+v", got)
	}
	for i, pcm := range v.emb.heard {
		// Over a second of 16 kHz 16-bit mono, and not silence.
		if len(pcm) < bridge.SampleRate*2 || strings.Count(string(pcm), "\x00") == len(pcm) {
			t.Errorf("take %d reached the sidecar as %d bytes", i+1, len(pcm))
		}
	}
}

// TestE2EEnrollingTeaganFromTheKitchensOwnClips: three of Teagan's refused
// wakes are still in the kitchen's log. The reviewer opens Voices on the
// kitchen, hears each clip before ticking it (every player says which clip
// it is, so a screen reader can tell them apart too), and enrolls her from
// the satellite's own audio.
//
// verifies SPEC §5, §9.2
func TestE2EEnrollingTeaganFromTheKitchensOwnClips(t *testing.T) {
	v := newVoicesVisit(t)
	v.emb.anyTake = []float32{0.4, 0.3, 0.2, 0.1}
	// Over the kettle, the dishwasher and the radio, a few minutes apart.
	for i, key := range []string{"wake/kitchen-kettle", "wake/kitchen-dishwasher", "wake/kitchen-radio"} {
		at := household.Day().Add(21*time.Hour + time.Duration(58+7*i)*time.Minute)
		v.keepWake(t, "kitchen", key, spokenClip(20, 4, i), "unknown_speaker", at)
	}
	p := open(t, v.s)
	p.visit(voicesRoute + "?satellite=kitchen")
	p.waitText("#enroll-clips", "Thu 9 Oct 22:12:00")

	var names []string
	p.eval(`[...document.querySelectorAll("#enroll-clips audio")].map(a => a.getAttribute("aria-label") || "")`, &names)
	if len(names) != 3 {
		t.Fatalf("the kitchen offers %d players, want its 3 clips", len(names))
	}
	want := []string{
		"Clip from Thu 9 Oct 22:12:00 · wake_rejected · unknown_speaker",
		"Clip from Thu 9 Oct 22:05:00 · wake_rejected · unknown_speaker",
		"Clip from Thu 9 Oct 21:58:00 · wake_rejected · unknown_speaker",
	}
	if !slices.Equal(names, want) {
		t.Errorf("the players are named %q, want each by its own clip, newest first: %q", names, want)
	}
	p.run(chromedp.SendKeys("#enroll-id", "teagan", chromedp.ByQuery), chromedp.SendKeys("#enroll-name", "Teagan", chromedp.ByQuery))
	for i := range 3 {
		p.click(fmt.Sprintf(`#enroll-clips .list__row:nth-of-type(%d) input[name=clip]`, i+1))
	}
	p.shot("voices-clips")

	resp, err := chromedp.RunResponse(p.ctx, chromedp.Evaluate(`document.querySelector("#enroll button[type=submit]").click()`, nil))
	if err != nil || resp == nil {
		t.Fatalf("enroll navigated nowhere: %v", err)
	}
	p.waitText("body", "Enrolled teagan.")
	if got := v.household(t).People; len(got) != 1 || got[0].ID != "teagan" || got[0].Utterances != 3 {
		t.Fatalf("identities.yaml holds %+v", got)
	}
}
