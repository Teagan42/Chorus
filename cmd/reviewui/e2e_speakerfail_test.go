//go:build e2e

package main

import (
	"fmt"
	"testing"
)

// Late on Thursday the speaker-ID sidecar went down. The reviewer opens the
// kitchen's log and finds Alan's "hey eddie" rejected as speaker_id_failed,
// with its clip playable and no verdict to give, since nothing judged his
// voice; then opens Teagan's office conversation and sees the ask heard
// during the outage marked as such, still hers (SPEC §5, §9.3, ADR-0097).
//
// verifies SPEC §5, §9.3
func TestE2EJourneyASpeakerIDOutageIsShownForWhatItIs(t *testing.T) {
	s, seq := outageNight(t)
	p := open(t, s)

	row := fmt.Sprintf("#seq-%d", seq)
	p.visit("/conversations/device:kitchen" + row)
	p.waitText(row, "wake rejected: speaker_id_failed")
	p.waitText(row, "speaker ID failed, so nothing judged the voice: no hard negative")
	p.shot("journey-speaker-id-failed-wake")

	p.visit("/conversations/" + convLamp)
	// Its third event: the opening, the lamp, then the fan.
	p.waitText("#seq-3", "and the desk fan")
	p.waitText("#seq-3", "speaker ID failed, so the turn stayed with whoever was speaking")
	p.shot("journey-speaker-id-failed-utterance")
}
