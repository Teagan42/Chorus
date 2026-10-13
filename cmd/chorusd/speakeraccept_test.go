package main

import (
	"testing"
)

// CHORUS_SPEAKER_ACCEPT moves the cosine at which a voice is the person it is
// nearest. A front end that rides its own gain compresses every score, so the
// default refuses a household's own short commands while still ranking them
// first; the threshold is the part that has to move (ADR-0084).
//
// verifies SPEC §5
func TestTheSpeakerAcceptThresholdIsConfigurable(t *testing.T) {
	for _, tc := range []struct {
		set     string
		want    float64
		refused bool
	}{
		{set: "", want: 0},
		{set: "0.5", want: 0.5},
		{set: "0.95", want: 0.95},
		{set: "1", refused: true},
		{set: "0", refused: true},
		{set: "-0.2", refused: true},
		{set: "loud", refused: true},
	} {
		got, err := (Config{SpeakerAccept: tc.set}).speakerAccept()
		switch {
		case tc.refused && err == nil:
			t.Errorf("%q was accepted as a threshold", tc.set)
		case !tc.refused && err != nil:
			t.Errorf("%q was refused: %v", tc.set, err)
		case !tc.refused && got != tc.want:
			t.Errorf("%q parsed to %v, want %v", tc.set, got, tc.want)
		}
	}
}
