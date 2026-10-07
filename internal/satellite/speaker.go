// Package satellite renders one session's speech onto one device.
//
// It exists to make the truncation point real. A barge-in's worth as a
// preference pair rests on the heard/unheard split being the DAC's own
// (SPEC §4.4, §15.1), and only the device knows where its DAC stopped. Every
// other implementation of session.Speaker infers the split from the length of
// the text, which is a guess the corpus cannot absorb.
package satellite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/session"
)

// Synth turns text into the device's playback format: 16 kHz, signed 16-bit,
// mono, little-endian. Kokoro fills this slot (SPEC §10).
type Synth interface {
	Synthesize(ctx context.Context, text string) ([]byte, error)
}

const bytesPerFrame = bridge.BitsPerSample / 8

// Downlink pacing. Audio is handed over a slice at a time and the feeder then
// waits for most of that slice to elapse, rather than writing an utterance at
// line rate: the device shares one radio between this downlink and the mic
// uplink, and saturating it looks exactly like a half-duplex failure from the
// host (SPEC §3.3.2). The prime is what keeps the mixer from underrunning
// before the pacing settles.
const (
	sliceDuration = 200 * time.Millisecond
	sliceBytes    = int(sliceDuration/time.Millisecond) * bridge.SampleRate / 1000 * bytesPerFrame
	paceFraction  = 3 // wait sliceDuration*(paceFraction-1)/paceFraction per slice
	primeSlices   = 2
)

// DefaultDrain bounds the wait for the DAC to confirm a finished utterance.
const DefaultDrain = 10 * time.Second

// deltaQueue is how many deltas may be awaiting synthesis. Generous because
// Write must not block, and a model's deltas are words.
const deltaQueue = 256

// Config wires one satellite. Everything that reads a clock or does I/O is
// injected, so `task test` stays hermetic (CONTRIBUTING §3).
type Config struct {
	Link   *bridge.Link
	Synth  Synth
	Timers session.Timers

	// Drain bounds the wait for the DAC to reach the end of a finished
	// utterance. Defaults to DefaultDrain.
	Drain time.Duration

	// OnMic, OnWake and OnMute forward the device's other uplink frames. A nil
	// hook drops its frames.
	OnMic  func(channel uint8, pcm []byte) error
	OnWake func(word string) error
	OnMute func(bridge.Mute) error
}

// Satellite is one device's speech output and playback position. It is both
// the link's bridge.Handler and the session's Speaker: the position the
// handler tracks is the number the Speaker truncates on.
type Satellite struct {
	cfg Config

	mu     sync.Mutex
	played uint64
	// notify is closed and replaced on every position change, so a waiter
	// cannot miss an advance between reading the position and sleeping.
	notify chan struct{}
}

// New validates the wiring and applies defaults.
func New(cfg Config) (*Satellite, error) {
	// Checked one at a time rather than through a map[string]any: a typed nil
	// pointer in an interface is not nil, so cfg.Link would pass.
	switch {
	case cfg.Link == nil:
		return nil, errors.New("satellite: link is required")
	case cfg.Synth == nil:
		return nil, errors.New("satellite: synth is required")
	case cfg.Timers == nil:
		return nil, errors.New("satellite: timers is required")
	}
	if cfg.Drain == 0 {
		cfg.Drain = DefaultDrain
	}
	return &Satellite{cfg: cfg, notify: make(chan struct{})}, nil
}

// OnPlayed records the DAC's cumulative position.
func (s *Satellite) OnPlayed(p bridge.Played) error {
	s.mu.Lock()
	// Monotonic by protocol: the firmware only resets on a new connection, and
	// a new connection is a new Satellite. A lower value is a desync, and
	// letting the position walk backwards would move a truncation point that
	// has already been journalled.
	if p.Frames > s.played {
		s.played = p.Frames
	}
	close(s.notify)
	s.notify = make(chan struct{})
	s.mu.Unlock()
	return nil
}

func (s *Satellite) OnMic(channel uint8, pcm []byte) error {
	if s.cfg.OnMic == nil {
		return nil
	}
	return s.cfg.OnMic(channel, pcm)
}

func (s *Satellite) OnWake(word string) error {
	if s.cfg.OnWake == nil {
		return nil
	}
	return s.cfg.OnWake(word)
}

func (s *Satellite) OnMute(m bridge.Mute) error {
	if s.cfg.OnMute == nil {
		return nil
	}
	return s.cfg.OnMute(m)
}

// position reports the DAC's cumulative frame count and a channel closed when
// it next changes.
func (s *Satellite) position() (uint64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.played, s.notify
}

// Open starts an utterance. Its frame offsets are relative to the position
// now, because PLAYED counts the whole connection.
func (s *Satellite) Open(ctx context.Context, callID string) (session.Stream, error) {
	base, _ := s.position()
	st := &stream{
		sat: s, ctx: ctx, callID: callID, base: base,
		in: make(chan int, deltaQueue), quit: make(chan struct{}), done: make(chan struct{}),
	}
	go st.feed()
	return st, nil
}

// segment is one delta and where its audio ends.
type segment struct {
	text string
	// end is the cumulative frame, relative to this utterance, at which the
	// delta finishes. Zero until synthesised: a delta cut before synthesis has
	// no frames but is still unspoken text the journal must carry (SPEC §4.2).
	end uint64
}

type stream struct {
	sat    *Satellite
	ctx    context.Context
	callID string
	base   uint64

	in   chan int      // indexes into segs, in generation order
	quit chan struct{} // Close asked the feeder to stop
	done chan struct{} // the feeder has finished

	mu     sync.Mutex
	segs   []segment
	total  uint64
	slices int
	closed bool
}

// Write queues a delta. It never blocks on synthesis or on the radio: the
// session calls this while holding the speech channel's lock
// (internal/session/speechchan.go), so a slow synthesiser here would stall
// every other child of the session.
func (s *stream) Write(text string) error {
	if text == "" {
		return nil
	}
	// Held across the send so the feeder's channel cannot be written after
	// Close has decided the utterance is over.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("satellite: write after close")
	}
	s.segs = append(s.segs, segment{text: text})
	select {
	case s.in <- len(s.segs) - 1:
		return nil
	default:
		return fmt.Errorf("satellite: %s has %d deltas awaiting synthesis", s.callID, deltaQueue)
	}
}

// feed synthesises and paces queued deltas until Close or a barge-in.
func (s *stream) feed() {
	defer close(s.done)
	for {
		select {
		case i := <-s.in:
			if !s.send(i) {
				return
			}
		case <-s.ctx.Done():
			return
		case <-s.quit:
			// Drain what is already queued: generation finished, so these are
			// deltas the user is still owed.
			for {
				select {
				case i := <-s.in:
					if !s.send(i) {
						return
					}
				default:
					return
				}
			}
		}
	}
}

// send renders one delta and paces it onto the link. False stops the feeder.
func (s *stream) send(i int) bool {
	s.mu.Lock()
	text := s.segs[i].text
	s.mu.Unlock()

	pcm, err := s.sat.cfg.Synth.Synthesize(s.ctx, text)
	if err != nil {
		// Not reported as an error: Close's contract is a truncation point, and
		// what the DAC played so far is still the honest answer.
		return false
	}

	s.mu.Lock()
	s.total += uint64(len(pcm) / bytesPerFrame)
	s.segs[i].end = s.total
	s.mu.Unlock()

	for off := 0; off < len(pcm); off += sliceBytes {
		if err := s.sat.cfg.Link.SendTTS(pcm[off:min(off+sliceBytes, len(pcm))]); err != nil {
			// ErrStopped means a barge-in overtook this payload, which is the
			// stop working, not a failure.
			return false
		}
		s.mu.Lock()
		s.slices++
		priming := s.slices <= primeSlices
		s.mu.Unlock()
		if priming {
			continue
		}
		select {
		case <-s.sat.cfg.Timers.After(sliceDuration * (paceFraction - 1) / paceFraction):
		case <-s.ctx.Done():
			return false
		}
	}
	return true
}

// Close ends the utterance and reports the split. It is called when
// *generation* ends, not when playback does (internal/session/speechchan.go),
// so on the normal path it waits for the DAC; a cancelled context is a
// barge-in and returns the cut immediately (SPEC §4.4).
func (s *stream) Close() session.Playback {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	close(s.quit)
	<-s.done

	if s.ctx.Err() == nil {
		_ = s.sat.cfg.Link.Finish()
		s.awaitDrain()
	}

	s.mu.Lock()
	segs := append([]segment(nil), s.segs...)
	total := s.total
	s.mu.Unlock()

	played := s.playedFrames()
	// Every delta rendered *and* the DAC reached the end of all of it. The
	// rendered test is not redundant: a delta cut before synthesis adds no
	// frames, so without it an utterance whose tail never reached the
	// synthesiser would report as fully spoken.
	if played >= total && rendered(segs) {
		// Clamped: a device that over-reports must not invent spoken text, and
		// the frame count the journal carries is this utterance's own audio.
		return session.Playback{Spoken: allText(segs), Frames: int64(total)}
	}
	// Discard what the device still holds. Left queued it would play over the
	// next utterance, and it is audio the barge-in already decided against.
	_ = s.sat.cfg.Link.Stop()
	spoken, unspoken := split(segs, played)
	return session.Playback{
		Spoken: spoken, Unspoken: unspoken,
		Frames: int64(played), Truncated: true,
	}
}

// playedFrames is the DAC's position within this utterance.
func (s *stream) playedFrames() uint64 {
	p, _ := s.sat.position()
	if p < s.base {
		return 0
	}
	return p - s.base
}

// awaitDrain waits for the DAC to confirm the whole utterance, bounded by
// Config.Drain. Without the wait every completed utterance would report as
// truncated, because Close runs the moment the model stops generating.
func (s *stream) awaitDrain() {
	deadline := s.sat.cfg.Timers.After(s.sat.cfg.Drain)
	for {
		s.mu.Lock()
		total := s.total
		s.mu.Unlock()
		played, changed := s.sat.position()
		if played-min(played, s.base) >= total {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			// Audio the device never confirmed is audio the user cannot be said
			// to have heard, so this falls through to the truncated report.
			return
		case <-s.ctx.Done():
			return
		}
	}
}

// rendered reports that every delta reached the synthesiser.
func rendered(segs []segment) bool {
	for _, seg := range segs {
		if seg.end == 0 {
			return false
		}
	}
	return true
}

func allText(segs []segment) string {
	var b strings.Builder
	for _, seg := range segs {
		b.WriteString(seg.text)
	}
	return b.String()
}

// split divides the utterance at the frame the DAC stopped on. A delta the DAC
// entered counts as spoken even if it was cut partway: calling it unspoken
// would record words as unheard that the user did hear, and a corpus trained
// on that learns to repeat itself (SPEC §4.4).
func split(segs []segment, played uint64) (spoken, unspoken string) {
	var heard, rest strings.Builder
	var start uint64
	for _, seg := range segs {
		if seg.end > 0 && played > start {
			heard.WriteString(seg.text)
			start = seg.end
			continue
		}
		rest.WriteString(seg.text)
	}
	return heard.String(), rest.String()
}
