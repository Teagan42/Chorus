package chat

import (
	"strings"

	"github.com/teagan42/chorus/internal/session"
)

// Speak is the one tool that is speech rather than an action. The model
// speaks only by calling it, which is the framing that survives the
// speech-to-speech adapter (SPEC §12, §4.1).
const Speak = "speak"

// Mode keeps an unrecognised mode from reaching the speech channel. The schema
// declares a closed enum, but neither endpoint validates arguments against it:
// qwen3:14b emits mode="filler" when the prompt suggests one. Queue is the only
// safe default, since preempt and interject discard or pause speech the user is
// still hearing (SPEC §4.2).
//
// This cannot catch a mode that is valid but wrong -- ornith:9b chose preempt
// to acknowledge a request, cutting off the speech it should have queued behind.
// Only the prompt can fix that.
func Mode(s string) session.Mode {
	switch m := session.Mode(s); m {
	case session.ModeQueue, session.ModePreempt, session.ModeInterject:
		return m
	default:
		return session.ModeQueue
	}
}

// Answer is what a turn that called nothing said in its content, to be
// spoken once it has finished; empty when that content is not an answer.
//
// Told to speak only through the speak tool, qwen3:14b still answers what it
// already knows -- a remembered garage code, last night's conversation, the
// day -- in content, reasoning that it "doesn't need any tools" (ADR-0046).
// That is an answer only when the model's reasoning went to its own field
// (reasoned) and the turn ended on its own (reason is stop): with thinking
// off, content can be the reasoning, which no one should hear. A turn that
// called anything has said what it meant to through its calls. The speak
// tool's name leading the content is the call the model meant to make, not
// words to say: unspoken (ADR-0046).
func Answer(content string, called, reasoned bool, reason string) string {
	if called || !reasoned || reason != "stop" {
		return ""
	}
	return unspoken(Unthought(content))
}

// unspoken drops the speak tool's name from the front of text, and the quotes
// around what follows it, as qwen3:14b writes the call it meant to make:
// "speak" on a line of its own, or speak "Would you like..." on one line.
// Text that merely starts with the word, such as "speaker", is left alone.
func unspoken(text string) string {
	rest, ok := strings.CutPrefix(text, Speak)
	if !ok || (rest != "" && !strings.ContainsAny(rest[:1], ": \t\n")) {
		return text
	}
	rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
	if len(rest) >= 2 && rest[0] == '"' && rest[len(rest)-1] == '"' {
		rest = strings.TrimSpace(rest[1 : len(rest)-1])
	}
	return rest
}
