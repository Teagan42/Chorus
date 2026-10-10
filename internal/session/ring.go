package session

import "strings"

// RingState is what a satellite's LED ring shows of its session: listening,
// working or speaking, as SPEC §3.3.1 asks, or nothing once it has ended.
type RingState string

const (
	RingOff       RingState = "off"
	RingListening RingState = "listening"
	RingThinking  RingState = "thinking"
	RingSpeaking  RingState = "speaking"
)

// Ring shows a session's state on its satellite (ADR-0056). Show is called
// with the session's lock held, so it must not block.
type Ring interface {
	Show(RingState)
}

// showLocked tells the ring when the live children change what it shows.
// Speaking outranks working, which outranks listening.
func (s *Session) showLocked() {
	if s.sup.cfg.Ring == nil {
		return
	}
	st := RingOff
	switch {
	case s.over:
	case s.children["speaking"] > 0:
		st = RingSpeaking
	case s.children["thinking"] > 0 || s.toolsLocked():
		st = RingThinking
	case s.children["listening"] > 0:
		st = RingListening
	}
	if st == s.shown {
		return
	}
	s.shown = st
	s.sup.cfg.Ring.Show(st)
}

// toolsLocked reports a tool call still running, a detached one included.
func (s *Session) toolsLocked() bool {
	for name, n := range s.children {
		if n > 0 && strings.HasPrefix(name, "tool:") {
			return true
		}
	}
	return false
}
