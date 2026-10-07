package satellite

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkingIsLossless(t *testing.T) {
	for _, text := range []string{
		"", "a", " ", "...", "Hello world.",
		"Sure, I can do that, but the kitchen light is already off.",
		"The weather today is sunny with a high of twenty degrees.",
		"It is 20.5 degrees and 1,000 lux — bright.",
		"naïve café — résumé",
		strings.Repeat("x", 200),
		strings.Repeat("ünïcödé ", 20),
	} {
		parts := chunk(text)
		// Every byte is spoken or unspoken, never both and never neither: the
		// two halves of a preference pair have to reconstruct what was said.
		if got := strings.Join(parts, ""); got != text {
			t.Errorf("chunk(%q) rejoins to %q", text, got)
		}
		for _, p := range parts {
			if p == "" {
				t.Errorf("chunk(%q) = %q, has an empty part", text, parts)
			}
			if !utf8.ValidString(p) {
				t.Errorf("chunk(%q) split a rune: %q", text, p)
			}
		}
	}
}

func TestChunkBreaksOnClauseAndSentenceBoundaries(t *testing.T) {
	for text, want := range map[string][]string{
		"Sure, I can do that, but it is off.": {"Sure, ", "I can do that, ", "but it is off."},
		"Yes. No. Maybe.":                     {"Yes. ", "No. ", "Maybe."},
		"Wait—no.":                            {"Wait—", "no."},
		"Hello world.":                        {"Hello world."},
	} {
		if got := chunk(text); !slices.Equal(got, want) {
			t.Errorf("chunk(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestChunkKeepsNumbersWhole(t *testing.T) {
	// A part is synthesised on its own, so "20." and "5 degrees" would be read
	// as two numbers.
	for _, text := range []string{"It is 20.5 degrees.", "About 1,000 lux."} {
		if got := chunk(text); len(got) != 1 {
			t.Errorf("chunk(%q) = %q, want it left whole", text, got)
		}
	}
}

func TestChunkBoundsASegmentThatHasNoPunctuation(t *testing.T) {
	text := strings.TrimSpace(strings.Repeat("word ", 40))
	for _, p := range chunk(text) {
		if len(p) > maxSegmentChars {
			t.Errorf("part %q is %d bytes, over the %d cap", p, len(p), maxSegmentChars)
		}
	}
}

func TestChunkWillNotCutInsideAWord(t *testing.T) {
	// One word longer than the cap. Exceeding the bound is the lesser evil: a
	// cut here would record half a token as heard.
	long := strings.Repeat("x", maxSegmentChars*2)
	if got := chunk(long); !slices.Equal(got, []string{long}) {
		t.Errorf("chunk of one long word = %q, want it left whole", got)
	}
}
