//go:build e2e

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/teagan42/chorus/internal/bridge"
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
