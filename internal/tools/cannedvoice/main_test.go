package main

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/satellite"
)

// The renderer is asked for every piece a canned line is cut into, once
// each, so a line the satellite streams finds all of its audio.
//
// verifies SPEC §7
func TestEverySegmentOfEveryLineIsListedOnce(t *testing.T) {
	lines := []string{
		"Sorry, I've lost my voice for a moment.",
		"Sorry, my hearing isn't working right now. Try again in a minute.",
	}
	m := list("af_heart", lines...)
	if m.Voice != "af_heart" {
		t.Errorf("voice = %q", m.Voice)
	}
	files := map[string]string{}
	for _, s := range m.Segments {
		if prev, dup := files[s.File]; dup {
			t.Errorf("%q and %q share %s", prev, s.Text, s.File)
		}
		files[s.File] = s.Text
		if s.File != satellite.FallbackName(s.Text) {
			t.Errorf("%q is listed as %s, want %s", s.Text, s.File, satellite.FallbackName(s.Text))
		}
	}
	listed := map[string]bool{}
	for _, s := range m.Segments {
		listed[s.Text] = true
	}
	for _, line := range lines {
		parts := satellite.Segments(line)
		if strings.Join(parts, "") != line {
			t.Fatalf("segments of %q do not rejoin to it: %q", line, parts)
		}
		for _, p := range parts {
			if !listed[p] {
				t.Errorf("%q of %q is not listed", p, line)
			}
		}
	}
	sorry := 0
	for _, s := range m.Segments {
		if s.Text == "Sorry, " {
			sorry++
		}
	}
	if sorry != 1 {
		t.Errorf("the shared opening is listed %d times, want once: %+v", sorry, m.Segments)
	}
}
