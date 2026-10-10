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
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/session"
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
	paceFraction  = 3 // wait (paceFraction-1)/paceFraction of each payload's own duration
	primeSlices   = 2
)

// paceFor is how long to wait after handing over n bytes: most of what that
// audio itself lasts. Proportional to the payload rather than a flat
// sliceDuration because a payload shorter than one slice -- the last of an
// utterance, or a short delta from a streaming model -- would otherwise be
// followed by a wait longer than the audio it paces, starving the mixer while
// the radio sits idle (SPEC §3.3.2).
func paceFor(n int) time.Duration {
	frames := time.Duration(n / bytesPerFrame)
	return frames * time.Second / bridge.SampleRate * (paceFraction - 1) / paceFraction
}

// DefaultDrain bounds the wait for the DAC to confirm a finished utterance.
const DefaultDrain = 10 * time.Second

// DefaultSettle bounds the wait for the device's answer to a stop. Short
// because it only delays the journal record, not the silence: the stop has
// already gone out by then. The device answers every stop, so this expires
// only when the link has failed under it.
const DefaultSettle = 250 * time.Millisecond

// deltaQueue is how many segments may be awaiting synthesis. Generous because
// Write must not block, and one delta becomes several segments.
const deltaQueue = 256

// Config wires one satellite. Everything that reads a clock or does I/O is
// injected, so `task test` stays hermetic (CONTRIBUTING §3).
type Config struct {
	Link   *bridge.Link
	Synth  Synth
	Timers session.Timers

	// Blobs keeps the audio that journal events reference. Required: speech
	// events are audio-bearing, and journal.Append rejects one without a
	// reference, so a satellite with nowhere to put audio cannot record what
	// it said (ADR-0007).
	Blobs blob.Store

	// Drain bounds the wait for the DAC to reach the end of a finished
	// utterance. Defaults to DefaultDrain.
	Drain time.Duration

	// Settle bounds the wait for the device's answer to a barge-in stop.
	// Defaults to DefaultSettle.
	Settle time.Duration

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
	// base is the DAC position the newest utterance's frames count from. Kept
	// after that utterance ends rather than cleared, so a candidate arriving
	// just behind the cut still resolves against the speech it interrupted.
	base uint64
	// stopped is the tag of the last stop the device has answered. The report
	// carrying it is the one taken after the device gated its counter, so it
	// is the only report that can place a cut (ADR-0033).
	stopped uint8
	// notify is closed and replaced on every report, so a waiter cannot miss
	// one between reading the position and sleeping.
	notify chan struct{}
	// starting closes on the first report past base: the newest utterance's
	// first frame reached the ear (ADR-0035). Nil once closed.
	starting chan struct{}

	// hungUp closes once the link is no longer read: no report can arrive.
	hungUp chan struct{}
	hangup sync.Once
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
	case cfg.Blobs == nil:
		return nil, errors.New("satellite: blobs is required")
	}
	if cfg.Drain == 0 {
		cfg.Drain = DefaultDrain
	}
	if cfg.Settle == 0 {
		cfg.Settle = DefaultSettle
	}
	return &Satellite{cfg: cfg, notify: make(chan struct{}), hungUp: make(chan struct{})}, nil
}

// Hangup says the link is no longer read, so a stream closing now stops
// waiting for a report and takes the position it has.
func (s *Satellite) Hangup() {
	s.hangup.Do(func() { close(s.hungUp) })
}

// OnPlayed records the DAC's cumulative position and which stop, if any, the
// report answers.
func (s *Satellite) OnPlayed(p bridge.Played) error {
	s.mu.Lock()
	// Monotonic by protocol: the firmware only resets on a new connection, and
	// a new connection is a new Satellite. A lower value is a desync, and
	// letting the position walk backwards would move a truncation point that
	// has already been journalled.
	if p.Frames > s.played {
		s.played = p.Frames
	}
	if p.Stop != 0 {
		s.stopped = p.Stop
	}
	if s.starting != nil && s.played > s.base {
		close(s.starting)
		s.starting = nil
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

// position reports the DAC's cumulative frame count and a channel closed on
// the next report.
func (s *Satellite) position() (uint64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.played, s.notify
}

// rebase takes the position an opening utterance counts from and publishes it
// in the same lock, so the base the listener reads is the base the stream kept.
// The channel it returns closes when the DAC first moves past that base.
func (s *Satellite) rebase() (uint64, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.base = s.played
	s.starting = make(chan struct{})
	return s.base, s.starting
}

// SpeechBase is the DAC position the newest utterance's frames count from.
// The Listening child subtracts it so a barge-in's recorded position is an
// offset into the speech it interrupted and not into the whole connection,
// which is what PLAYED counts (SPEC §3.2.1, ADR-0030).
func (s *Satellite) SpeechBase() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

// answered reports the last stop tag the device has answered and a channel
// closed on the next report.
func (s *Satellite) answered() (uint8, <-chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopped, s.notify
}

// Open starts an utterance. Its frame offsets are relative to the position
// now, because PLAYED counts the whole connection.
func (s *Satellite) Open(ctx context.Context, callID string) (session.Stream, error) {
	// Keyed on an id of our own rather than the call id. An engine only
	// promises a call id groups one utterance's deltas, so a later turn may
	// reuse one, and a reused key would silently repoint every earlier journal
	// reference at the new audio. The event carrying the reference is what ties
	// it back to the call (SPEC §8).
	w, err := s.cfg.Blobs.Create(ctx, "tts/"+rand.Text())
	if err != nil {
		return nil, fmt.Errorf("satellite: open audio for %s: %w", callID, err)
	}
	base, started := s.rebase()
	st := &stream{
		sat: s, ctx: ctx, callID: callID, base: base, started: started, audio: w,
		in: make(chan int, deltaQueue), quit: make(chan struct{}), done: make(chan struct{}),
	}
	go st.feed()
	return st, nil
}

// segment is one delta and where its audio ends.
type segment struct {
	text string
	// end is the cumulative frame, relative to this utterance, at which the
	// delta finishes. A delta cut before synthesis has no frames but is still
	// unspoken text the journal must carry (SPEC §4.2).
	end uint64
	// synthesised is set once the synthesiser returned for this delta. Not
	// inferred from end: blank text renders as zero frames, so a leading
	// whitespace delta has end == 0 and was still rendered.
	synthesised bool
}

type stream struct {
	sat    *Satellite
	ctx    context.Context
	callID string
	base   uint64

	// started closes on the device's first report past base.
	started <-chan struct{}

	in   chan int      // indexes into segs, in generation order
	quit chan struct{} // Close asked the feeder to stop
	done chan struct{} // the feeder has finished

	// audio accumulates everything rendered for this utterance. Written only
	// by the feeder, committed by Close once the feeder has stopped.
	audio blob.Writer

	mu     sync.Mutex
	segs   []segment
	total  uint64
	slices int
	closed bool
	// overflowed is set once a segment could not be queued. Every segment after
	// it is recorded but never synthesised.
	overflowed bool
	// failed is why the feeder stopped short of a cut: the synthesiser, or the
	// store the audio is kept in, failed. Reported, never as a barge-in.
	failed error
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
	// Cut into segments here rather than at synthesis, because a segment is the
	// unit the truncation point resolves to (see maxSegmentChars). Every part is
	// recorded before any is queued: an overflow partway through must leave the
	// rest of the delta in the record as unspoken text, not drop it.
	first := len(s.segs)
	for _, part := range chunk(text) {
		s.segs = append(s.segs, segment{text: part})
	}
	if s.overflowed {
		return s.overflowErr()
	}
	for i := first; i < len(s.segs); i++ {
		select {
		case s.in <- i:
		default:
			// Nothing after this segment may be queued, by this Write or a later
			// one: the feeder would play past the hole and skip words mid-sentence.
			// The unqueued segments keep no frames, so they report as unspoken.
			s.overflowed = true
			return s.overflowErr()
		}
	}
	return nil
}

// fail keeps the first reason the feeder stopped.
func (s *stream) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = err
	}
}

func (s *stream) overflowErr() error {
	return fmt.Errorf("satellite: %s has %d segments awaiting synthesis", s.callID, deltaQueue)
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
		// Not returned as an error: Close's contract is a truncation point, and
		// what the DAC played so far is still the honest answer. Kept as the
		// reason, unless a barge-in cancelled the synthesis (ADR-0051).
		s.fail(fmt.Errorf("synthesize %q: %w", text, err))
		return false
	}

	// Everything rendered is kept, including audio a barge-in stopped before
	// the DAC reached it: Frames marks where the ear stopped, so a reader can
	// trim, and the untrimmed tail is the unspoken half of the pair (SPEC §9.1).
	if _, err := s.audio.Write(pcm); err != nil {
		s.fail(fmt.Errorf("keep audio: %w", err))
		return false
	}

	s.mu.Lock()
	s.total += uint64(len(pcm) / bytesPerFrame)
	s.segs[i].end = s.total
	s.segs[i].synthesised = true
	s.mu.Unlock()

	for off := 0; off < len(pcm); off += sliceBytes {
		slice := pcm[off:min(off+sliceBytes, len(pcm))]
		if err := s.sat.cfg.Link.SendTTS(slice); err != nil {
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
		case <-s.sat.cfg.Timers.After(paceFor(len(slice))):
		case <-s.ctx.Done():
			return false
		}
	}
	return true
}

var _ session.Starter = (*stream)(nil)

// Started closes when the device first reports playing this utterance's
// audio, which is when the household starts hearing it (ADR-0035).
func (s *stream) Started() <-chan struct{} { return s.started }

// Close ends the utterance and reports the split. It is called when
// *generation* ends, not when playback does (internal/session/speechchan.go),
// so on the normal path it waits for the DAC; a cancelled context is a
// barge-in and returns the cut immediately (SPEC §4.4).
func (s *stream) Close() session.Playback {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()

	cut := s.ctx.Err() != nil
	var tag uint8
	var stopErr error
	if cut {
		// Sent first, before the feeder is even asked to exit. The device is
		// playing buffered audio right now and this is the frame that silences
		// it, so nothing may precede it: Synth is an injected interface,
		// possibly an HTTP call to Kokoro, and a feeder parked inside it would
		// otherwise hold the user's ears for that call's whole duration.
		// Barge-in latency cannot depend on a synthesiser honouring its
		// context (SPEC §4.4).
		tag, stopErr = s.sat.cfg.Link.Stop()
	}
	close(s.quit)
	<-s.done

	switch {
	case !cut:
		_ = s.sat.cfg.Link.Finish()
		s.awaitDrain()
	case stopErr == nil:
		s.settle(tag)
	}

	s.mu.Lock()
	segs := append([]segment(nil), s.segs...)
	total := s.total
	failed := s.failed
	s.mu.Unlock()

	// Read after the device has answered the stop, never before: the DAC keeps
	// going for the stop's flight time, and the firmware gates its counter at
	// the stop precisely so that the report answering it is the truncation
	// point. A stop the link could not deliver has no answer to wait for, and
	// the position already known is then the honest one.
	played := s.playedFrames()
	ref := s.keepAudio(played > 0)
	// Every delta rendered *and* the DAC reached the end of all of it. The
	// rendered test is not redundant: a delta cut before synthesis adds no
	// frames, so without it an utterance whose tail never reached the
	// synthesiser would report as fully spoken.
	if played >= total && rendered(segs) {
		// Clamped: a device that over-reports must not invent spoken text, and
		// the frame count the journal carries is this utterance's own audio.
		return session.Playback{Spoken: allText(segs), AudioRef: ref, Frames: int64(total)}
	}
	var failure error
	if !cut {
		// Truncated without a barge-in: the synthesiser failed, or the drain
		// deadline expired. Discard what the device still holds, or it plays
		// over the next utterance. No settle after this one -- either the
		// device has drained what it was sent, or it reported nothing for the
		// whole drain window, so there is nothing in flight to wait for.
		_, _ = s.sat.cfg.Link.Stop()
		failure = failureOf(failed)
	}
	spoken, unspoken := split(segs, played)
	return session.Playback{
		Spoken: spoken, Unspoken: unspoken, AudioRef: ref,
		Frames: int64(played), Truncated: true, Failure: failure,
	}
}

// failureOf names why an uncut utterance stopped short: the voice, when the
// feeder stopped on an error, else a device that never confirmed the end.
// Neither is the person interrupting, and the session must not record either
// as one (ADR-0051).
func failureOf(failed error) error {
	if failed != nil {
		return fmt.Errorf("%w: %w", session.ErrVoiceUnavailable, failed)
	}
	return session.ErrPlaybackUnconfirmed
}

// keepAudio publishes the utterance's audio, or discards it when no event will
// name it. Safe either way only once the feeder has stopped, since the feeder
// is what writes.
//
// Audio nothing was heard of is dropped: the session journals that as
// speech_discarded, which carries no reference (schema/event.cue), so a blob
// kept here would be one no event names -- unreachable by replay or export,
// and still occupying disk. An empty ref on a failed commit is deliberate too:
// the audio is gone, journal.Append rejects an audio-bearing event without a
// reference, and that loud failure beats a record pointing at nothing.
func (s *stream) keepAudio(keep bool) string {
	if !keep {
		_ = s.audio.Abort()
		return ""
	}
	ref, _ := s.audio.Commit()
	return ref
}

// playedFrames is the DAC's position within this utterance.
func (s *stream) playedFrames() uint64 {
	p, _ := s.sat.position()
	if p < s.base {
		return 0
	}
	return p - s.base
}

// settle waits for the report that answers this stop. The DAC keeps emitting
// for as long as the stop takes to arrive, and those frames were heard, so the
// cut has to be the position the device gated when the stop reached it. That
// position is on the one report echoing the stop's tag, and on no other: a
// routine report emitted just before the stop can still be in flight when it
// is sent, and the first change to arrive would then be a round trip stale
// (ADR-0033).
//
// Bounded and allowed to expire: the context is already cancelled, and a link
// that fails under the stop never answers. A link nobody reads any more cannot
// either, so a hangup ends the wait too.
// A timeout falls back to the position already known: never worse than not
// waiting.
func (s *stream) settle(tag uint8) {
	deadline := s.sat.cfg.Timers.After(s.sat.cfg.Settle)
	for {
		stopped, next := s.sat.answered()
		if stopped == tag {
			return
		}
		select {
		case <-next:
		case <-deadline:
			return
		case <-s.sat.hungUp:
			return
		}
	}
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
		case <-s.sat.hungUp:
			return
		}
	}
}

// rendered reports that every delta reached the synthesiser.
func rendered(segs []segment) bool {
	for _, seg := range segs {
		if !seg.synthesised {
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
		if seg.synthesised && played > start {
			heard.WriteString(seg.text)
			start = seg.end
			continue
		}
		rest.WriteString(seg.text)
	}
	return heard.String(), rest.String()
}
