package session

import (
	"strings"
	"unicode"
)

// HotWord is a phrase the session acts on without asking the model, as
// hot_word records it (ADR-0064).
type HotWord string

const (
	// HotStop stops the speech playing, as any barge-in does.
	HotStop HotWord = "stop"
	// HotNeverMind stops speech too, and reaches a turn still working
	// silently, which it ends under each call's on_interrupt policy.
	HotNeverMind HotWord = "never_mind"
	// HotRepeat says again what the person last heard the assistant say.
	HotRepeat HotWord = "repeat"
)

// Hot reports which hot phrase text is, or empty. The whole text must be
// the phrase: "stop the music in the kitchen" is a request, not a stop.
func Hot(text string) HotWord {
	words := strings.Fields(normalize(text))
	for len(words) > 0 && leading(words[0]) {
		words = words[1:]
	}
	return hotPhrase(once(words))
}

// hotPhrase is the closed set. Each entry stops the house on one word, past
// the gate's word-count stage, so it stays small; English-only, as the ear
// is (SPEC §10).
func hotPhrase(phrase string) HotWord {
	switch phrase {
	case "stop", "cancel", "quiet", "enough", "shut up", "be quiet", "stop it", "thats enough":
		return HotStop
	case "never mind", "nevermind", "forget it", "forget that":
		return HotNeverMind
	case "say that again", "repeat that", "what did you say", "come again":
		return HotRepeat
	}
	return ""
}

// leading is what may come before a hot phrase without changing it: the
// assistant's name said without the wake word, or an "okay".
func leading(word string) bool {
	switch word {
	case "hey", "eddie", "okay", "ok":
		return true
	}
	return false
}

// once folds a phrase said over and over, "stop stop stop", into one saying
// of it. Anything else is joined as it was said.
func once(words []string) string {
	for n := 1; n < len(words); n++ {
		if len(words)%n != 0 {
			continue
		}
		repeated := true
		for i := n; i < len(words) && repeated; i++ {
			repeated = words[i] == words[i%n]
		}
		if repeated {
			return strings.Join(words[:n], " ")
		}
	}
	return strings.Join(words, " ")
}

// normalize lowercases text and drops punctuation, so the ear's "Stop!"
// and "That's enough." match. Apostrophes go without a space.
func normalize(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\'' || r == '’':
			return -1
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return unicode.ToLower(r)
		}
		return ' '
	}, text)
}
