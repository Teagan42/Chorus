package satellite

import (
	"unicode"
	"unicode/utf8"
)

// maxSegmentChars bounds a run of text with no punctuation to break on, so a
// cut inside one segment over-credits at most this much as heard.
//
// A segment counts as wholly spoken once the DAC enters it (see split), so an
// utterance delivered as a single segment -- which is what the speak tool does,
// a whole sentence per call (internal/session/session.go) -- reports a
// mid-sentence cut as the entire sentence spoken. Kokoro reports no per-word
// frame offsets, so a clause is the finest boundary available without inferring
// the split from the length of the text, which is the guess this package exists
// to avoid (SPEC §3.2.1, §15).
const maxSegmentChars = 32

// chunk divides a delta into the pieces a truncation point may fall between.
// The parts rejoin to text exactly.
func chunk(text string) []string {
	var parts []string
	for rest := text; rest != ""; {
		i := breakAt(rest)
		parts = append(parts, rest[:i])
		rest = rest[i:]
	}
	return parts
}

// breakAt is the byte index to cut s at, always past its first rune so the
// caller makes progress.
func breakAt(s string) int {
	var word int // byte end of the last word boundary seen
	for i, r := range s {
		end := i + utf8.RuneLen(r)
		switch {
		case isDash(r):
			return end + spaceRun(s[end:])
		case isStop(r):
			// Only before whitespace or at the end, so "20.5" and "1,000" stay
			// whole: each part is synthesised on its own, and a number split
			// across two of them is read as two numbers.
			if run := spaceRun(s[end:]); run > 0 || end == len(s) {
				return end + run
			}
		case unicode.IsSpace(r):
			word = end
		}
		// Past the cap, fall back to the last word boundary. With none yet, read
		// on: a part longer than the cap beats one cut inside a word.
		if end >= maxSegmentChars && word > 0 {
			return word
		}
	}
	return len(s)
}

func isStop(r rune) bool {
	switch r {
	case '.', '!', '?', ',', ';', ':':
		return true
	}
	return false
}

// isDash covers the em and en dashes, which break without a following space.
// The ASCII hyphen does not: it joins words.
func isDash(r rune) bool { return r == '—' || r == '–' }

// spaceRun is the length of the whitespace starting s. Trailing space belongs
// with the clause it follows, so no part begins with it.
func spaceRun(s string) int {
	for i, r := range s {
		if !unicode.IsSpace(r) {
			return i
		}
	}
	return len(s)
}
