package main

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// The kitchen's barge-in blob is alice's whole utterance, correction and all:
// only its judged prefix is the barge-in.
func bargeInWithinItsUtterance() harvest.Pair {
	return harvest.Pair{
		ID: "pair-kitchen", BargeInPositionMS: 900, BargeInFrames: 8000,
		Audio: harvest.Audio{BargeIn: "blob://mic/3", Correction: "blob://mic/3"},
	}
}

// verifies SPEC §9.2
func TestTheBargeInClipStopsWhereTheGateStoppedListening(t *testing.T) {
	var src string
	for _, c := range clips(bargeInWithinItsUtterance()) {
		if c.Label == "barge-in" {
			src = c.Src
		}
	}
	if want := audioSrc("blob://mic/3") + "&to=8000"; src != want {
		t.Errorf("barge-in clip = %q, want %q: it would replay the correction too", src, want)
	}
}

// verifies SPEC §9.2
func TestTheBargeInRegionIsAsLongAsWhatTheGateJudged(t *testing.T) {
	p := bargeInWithinItsUtterance()
	l := layout(p, map[string]int{"blob://mic/3": 3000})
	if got, want := l.cut-l.bargeAt, framesMS(8000); got != want {
		t.Errorf("barge-in region = %d ms, want %d: the whole utterance is not the barge-in", got, want)
	}
}

// verifies SPEC §9.2
func TestTheLogPlaysOnlyTheJudgedPartOfABargeIn(t *testing.T) {
	start := journal.Event{Seq: 1, Kind: journal.KindSessionOpened, At: time.Unix(1_760_000_000, 0)}
	for _, kind := range []journal.Kind{journal.KindBargeInDetected, journal.KindBargeInRejected} {
		e := journal.Event{
			Seq: 2, Kind: kind, At: start.At.Add(time.Second), AudioRef: "blob://mic/3",
			Fields: map[string]string{"tts_position_ms": "900", "stage": "speaker_id", "audio_frames": "8000"},
		}
		if got := logRowOf(e, start, nil).Audio; !strings.HasSuffix(got, url.QueryEscape("blob://mic/3")+"&to=8000") {
			t.Errorf("%s audio = %q, want it bounded at the judged frames", kind, got)
		}
	}
}
