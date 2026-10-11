package session_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/session"
)

// What the household says to a voice assistant, as the ear writes it down.
// A hot phrase is the whole of what was said; one inside a request is the
// request.
//
// verifies SPEC §4.3
func TestHotPhrasesAreTheWholeOfWhatWasSaid(t *testing.T) {
	for _, c := range []struct {
		said string
		want session.HotWord
	}{
		{"Stop.", session.HotStop},
		{"stop!", session.HotStop},
		{"Cancel", session.HotStop},
		{"Quiet.", session.HotStop},
		{"Enough!", session.HotStop},
		{"Shut up.", session.HotStop},
		{"Be quiet", session.HotStop},
		{"Stop it.", session.HotStop},
		{"That's enough.", session.HotStop},
		{"That’s enough", session.HotStop},
		{"Eddie, stop.", session.HotStop},
		{"Okay, stop.", session.HotStop},
		{"OK Eddie stop", session.HotStop},
		{"Hey Eddie, be quiet.", session.HotStop},
		{"Stop, stop.", session.HotStop},
		{"Never mind.", session.HotNeverMind},
		{"Nevermind", session.HotNeverMind},
		{"Forget it.", session.HotNeverMind},
		{"Eddie, forget that.", session.HotNeverMind},
		{"Say that again?", session.HotRepeat},
		{"Repeat that.", session.HotRepeat},
		{"What did you say?", session.HotRepeat},
		{"Come again?", session.HotRepeat},

		{"Stop the music in the kitchen.", ""},
		{"Cancel the oven timer.", ""},
		{"Never mind the lights, what's the weather?", ""},
		{"Don't stop.", ""},
		{"Uh.", ""},
		{"Okay.", ""},
		{"Eddie.", ""},
		{"", ""},
		{"Stop. Cancel.", ""},
	} {
		if got := session.Hot(c.said); got != c.want {
			t.Errorf("Hot(%q) = %q, want %q", c.said, got, c.want)
		}
	}
}
