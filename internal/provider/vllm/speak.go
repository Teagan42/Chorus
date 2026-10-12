package vllm

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// speakSoFar is what a speak call's arguments say so far, read out of JSON
// that has not finished arriving.
type speakSoFar struct {
	// Text is the text argument's words that have arrived whole: an escape
	// cut in half, a surrogate waiting for its pair, or a rune whose bytes
	// have not all come is held back until it is.
	Text string
	// Started is whether the text argument's value has begun.
	Started bool
	// Mode is the mode argument, once its string has closed.
	Mode      string
	ModeKnown bool
}

// readSpeak reads a speak call's arguments as far as they go. It is a
// forgiving reader of an object's prefix, not a validator: the call is
// unmarshalled properly once its JSON closes, and that is what is kept.
//
// Arguments are short, so each fragment rereads them from the start rather
// than keeping a tokenizer's state between fragments.
func readSpeak(args string) speakSoFar {
	var out speakSoFar
	i := skipSpace(args, 0)
	if i >= len(args) || args[i] != '{' {
		return out
	}
	i++
	for {
		i = skipSpace(args, i)
		if i >= len(args) || args[i] == '}' {
			return out
		}
		if args[i] == ',' {
			i++
			continue
		}
		if args[i] != '"' {
			return out
		}
		key, end, ok := readString(args, i)
		if !ok {
			return out
		}
		i = skipSpace(args, end)
		if i >= len(args) || args[i] != ':' {
			return out
		}
		i = skipSpace(args, i+1)
		if i >= len(args) {
			return out
		}
		switch {
		case key == "text" && args[i] == '"':
			text, end, ok := readString(args, i)
			out.Text, out.Started = text, true
			if !ok {
				return out
			}
			i = end
		case key == "mode" && args[i] == '"':
			mode, end, ok := readString(args, i)
			if !ok {
				return out
			}
			out.Mode, out.ModeKnown, i = mode, true, end
		default:
			end, ok := skipValue(args, i)
			if !ok {
				return out
			}
			i = end
		}
	}
}

// readString decodes the JSON string opening at s[i], as far as it has
// arrived. ok is whether it closed; end is just past its closing quote.
func readString(s string, i int) (text string, end int, ok bool) {
	var b strings.Builder
	j := i + 1
	for j < len(s) {
		c := s[j]
		switch {
		case c == '"':
			return b.String(), j + 1, true
		case c == '\\':
			r, n, whole := unescape(s[j:])
			if !whole {
				return b.String(), j, false
			}
			b.WriteRune(r)
			j += n
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			j++
		default:
			if !utf8.FullRuneInString(s[j:]) {
				return b.String(), j, false
			}
			r, n := utf8.DecodeRuneInString(s[j:])
			b.WriteRune(r)
			j += n
		}
	}
	return b.String(), j, false
}

// unescape decodes the escape at the front of s, and how many bytes it
// took. whole is false while the escape, or the low half of a surrogate
// pair, has not all arrived.
func unescape(s string) (r rune, n int, whole bool) {
	if len(s) < 2 {
		return 0, 0, false
	}
	switch s[1] {
	case '"', '\\', '/':
		return rune(s[1]), 2, true
	case 'b':
		return '\b', 2, true
	case 'f':
		return '\f', 2, true
	case 'n':
		return '\n', 2, true
	case 'r':
		return '\r', 2, true
	case 't':
		return '\t', 2, true
	case 'u':
		if len(s) < 6 {
			return 0, 0, false
		}
		hi, ok := hex4(s[2:6])
		if !ok {
			return utf8.RuneError, 2, true
		}
		if !utf16.IsSurrogate(hi) {
			return hi, 6, true
		}
		// A high surrogate means nothing until its low half arrives, if
		// what follows can still be one.
		pair := len(s) >= 8 && s[6] == '\\' && s[7] == 'u'
		if !pair && (len(s) == 6 || (len(s) == 7 && s[6] == '\\')) {
			return 0, 0, false
		}
		if pair && len(s) < 12 {
			return 0, 0, false
		}
		if pair {
			if lo, ok := hex4(s[8:12]); ok {
				if r := utf16.DecodeRune(hi, lo); r != utf8.RuneError {
					return r, 12, true
				}
			}
		}
		return utf8.RuneError, 6, true
	default:
		// Not JSON: kept as written, for the full unmarshal to refuse.
		return rune(s[1]), 2, true
	}
}

// hex4 reads the four hex digits of a \u escape.
func hex4(s string) (rune, bool) {
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return 0, false
	}
	return rune(v), true
}

// skipValue skips the JSON value opening at s[i]. ok is false when it has
// not finished arriving; a number or literal at the very end may yet go on.
func skipValue(s string, i int) (end int, ok bool) {
	switch s[i] {
	case '"':
		_, end, ok := readString(s, i)
		return end, ok
	case '{', '[':
		depth := 0
		for j := i; j < len(s); j++ {
			switch s[j] {
			case '"':
				_, end, ok := readString(s, j)
				if !ok {
					return end, false
				}
				j = end - 1
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return j + 1, true
				}
			}
		}
		return len(s), false
	default:
		for j := i; j < len(s); j++ {
			switch s[j] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return j, true
			}
		}
		return len(s), false
	}
}

func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}
