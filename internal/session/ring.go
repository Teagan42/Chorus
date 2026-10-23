package session

import (
	"strings"
	"sync"
)

// RingState is what a satellite's LED ring shows: its session listening,
// working or speaking, as SPEC §3.3.1 asks, the device muted, a failure
// the household should know about, or nothing.
type RingState string

const (
	RingOff       RingState = "off"
	RingListening RingState = "listening"
	RingThinking  RingState = "thinking"
	RingSpeaking  RingState = "speaking"
	RingMuted     RingState = "muted"
	RingError     RingState = "error"
)

// Ring shows a session's state on its satellite (ADR-0056). Show is called
// with the session's lock held, so it must not block.
type Ring interface {
	Show(RingState)
}

// Fault is a dependency a satellite needs that was last seen failing.
type Fault string

const (
	FaultEar     Fault = "stt"
	FaultJournal Fault = "journal"
	FaultModel   Fault = "model"
	FaultVoice   Fault = "voice"

	// FaultSpeakerID is the speaker embedder failing: no spoken wake can be
	// confirmed while it is, and the rejection says nothing (ADR-0097).
	FaultSpeakerID Fault = "speaker_id"
)

// Faults is a Ring that is also told what is down and what came back. A
// ring that is not shows only the session (ADR-0069).
type Faults interface {
	Fault(Fault)
	Recover(Fault)
}

// Panel is one satellite's ring shown everything it should show: the
// device's mute over all else, then its session, then any fault. A fault
// stays until its dependency next succeeds (ADR-0069).
type Panel struct {
	ring Ring

	mu      sync.Mutex
	session RingState
	muted   bool
	faults  map[Fault]bool
	shown   RingState
}

// NewPanel drives ring, which starts dark.
func NewPanel(ring Ring) *Panel {
	return &Panel{ring: ring, session: RingOff, shown: RingOff, faults: map[Fault]bool{}}
}

// Show takes a session's state. Like every Panel method it never blocks.
func (p *Panel) Show(st RingState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.session = st
	p.showLocked()
}

// Mute takes the device's own report of its hardware or software mute.
func (p *Panel) Mute(on bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.muted = on
	p.showLocked()
}

// Fault marks f down.
func (p *Panel) Fault(f Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.faults[f] = true
	p.showLocked()
}

// Recover marks f answering again.
func (p *Panel) Recover(f Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.faults[f] {
		return
	}
	delete(p.faults, f)
	p.showLocked()
}

func (p *Panel) showLocked() {
	st := RingOff
	switch {
	case p.muted:
		st = RingMuted
	case p.session != RingOff:
		st = p.session
	case len(p.faults) > 0:
		st = RingError
	}
	if st == p.shown {
		return
	}
	p.shown = st
	p.ring.Show(st)
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

// fault tells a ring that shows faults that f failed.
func (sup *Supervisor) fault(f Fault) {
	if r, ok := sup.cfg.Ring.(Faults); ok {
		r.Fault(f)
	}
}

// answered tells a ring that shows faults that f answered.
func (sup *Supervisor) answered(f Fault) {
	if r, ok := sup.cfg.Ring.(Faults); ok {
		r.Recover(f)
	}
}
