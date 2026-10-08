package stt

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
)

// ErrTooLong reports a write that would take the utterance past its bound.
// The audio already held is kept and Finish still decodes it.
var ErrTooLong = errors.New("stt: utterance exceeds its bound")

// Options tunes one utterance. Zero values take the package defaults.
type Options struct {
	// PartialEvery is the byte count of new audio between re-decodes. A count
	// rather than a timer: it makes the cadence a property of the audio, so a
	// test can drive it without a clock and a stalled uplink produces no
	// partials rather than a stream of identical ones.
	PartialEvery int

	// MaxBytes bounds the buffer. Defaults to DefaultMaxUtterance.
	MaxBytes int
}

// Utterance is a streaming adapter over a batch Transcriber. It accepts PCM
// as the bridge delivers it, re-decodes the whole buffer so far every
// PartialEvery bytes, and decodes it once more on Finish. The whisper_streaming
// family of systems works the same way (ADR-0024).
//
// Write never blocks on the endpoint: the decode runs on one goroutine the
// utterance owns, which exits when the context ends (CONTRIBUTING §6).
type Utterance struct {
	t    Transcriber
	ctx  context.Context
	opts Options

	mu       sync.Mutex
	buf      []byte
	pending  int
	finished bool
	// partialCancel aborts the decode in flight, so a Finish does not wait
	// behind a partial whose text the final is about to replace (SPEC §11).
	partialCancel context.CancelFunc

	// kick holds at most one wake-up. Writes that land during a decode collapse
	// into the one re-decode that follows it, which reads the whole buffer
	// anyway; queueing one per cadence would only ever lag further behind.
	kick     chan struct{}
	finish   chan struct{}
	final    chan outcome
	partials chan Result
	done     chan struct{}
}

type outcome struct {
	res Result
	err error
}

// Open starts an utterance and its decode goroutine.
func Open(ctx context.Context, t Transcriber, opts Options) (*Utterance, error) {
	if t == nil {
		return nil, errors.New("stt: transcriber is required")
	}
	if opts.PartialEvery <= 0 {
		opts.PartialEvery = DefaultPartialEvery
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxUtterance
	}
	u := &Utterance{
		t: t, ctx: ctx, opts: opts,
		kick:     make(chan struct{}, 1),
		finish:   make(chan struct{}),
		final:    make(chan outcome, 1),
		partials: make(chan Result, 1),
		done:     make(chan struct{}),
	}
	go u.run()
	return u, nil
}

// Write appends device audio. It is called from the bridge's read loop, so it
// only ever touches the buffer (internal/bridge/link.go).
func (u *Utterance) Write(pcm []byte) error {
	// An odd byte is a torn sample, and every sample after it would be a byte
	// out of phase: the endpoint would hear noise, not speech.
	if len(pcm)%bytesPerFrame != 0 {
		return fmt.Errorf("stt: %d bytes is not whole samples", len(pcm))
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.finished {
		return errors.New("stt: write after finish")
	}
	if len(u.buf)+len(pcm) > u.opts.MaxBytes {
		return ErrTooLong
	}
	u.buf = append(u.buf, pcm...)
	u.pending += len(pcm)
	if u.pending < u.opts.PartialEvery {
		return nil
	}
	u.pending = 0
	select {
	case u.kick <- struct{}{}:
	default:
	}
	return nil
}

// Partials yields the latest partial. The channel holds one result and a
// newer one replaces an unread older one: the gate asks "what has been said so
// far", and a backlog of stale answers is worse than none (SPEC §4.3).
func (u *Utterance) Partials() <-chan Result { return u.partials }

// Done closes once the decode goroutine has exited.
func (u *Utterance) Done() <-chan struct{} { return u.done }

// Finish ends the utterance and returns its final decode. A failed partial is
// dropped because the next decode covers the same audio; a failed final is
// reported, because nothing follows it. An utterance whose context was
// cancelled fails cleanly rather than returning its last partial: a session
// that ended no longer wants the turn, and a partial is not the utterance.
func (u *Utterance) Finish(ctx context.Context) (Result, error) {
	u.mu.Lock()
	if u.finished {
		u.mu.Unlock()
		return Result{}, errors.New("stt: utterance already finished")
	}
	u.finished = true
	cancel := u.partialCancel
	u.mu.Unlock()

	close(u.finish)
	if cancel != nil {
		cancel()
	}
	select {
	case o := <-u.final:
		return o.res, o.err
	case <-u.done:
		// Both may be ready at once: the goroutine sends the final and then
		// exits, so the final is checked before the exit is read as a cancel.
		select {
		case o := <-u.final:
			return o.res, o.err
		default:
		}
		return Result{}, fmt.Errorf("finish utterance: %w", u.ctx.Err())
	case <-ctx.Done():
		return Result{}, fmt.Errorf("finish utterance: %w", ctx.Err())
	}
}

// run is the one decode goroutine. Partials are sequential: a decode is a
// whole round trip over a growing buffer, and two in flight would only race
// to report the same audio.
func (u *Utterance) run() {
	defer close(u.done)
	for {
		select {
		case <-u.ctx.Done():
			return
		case <-u.finish:
			u.final <- u.decodeFinal()
			return
		case <-u.kick:
			// A kick and a finish ready together: the final supersedes the
			// partial, and running the partial first only delays it.
			select {
			case <-u.finish:
				u.final <- u.decodeFinal()
				return
			default:
			}
			u.partial()
		}
	}
}

func (u *Utterance) partial() {
	u.mu.Lock()
	// Copied because Write keeps appending to the buffer under the lock.
	snap := slices.Clone(u.buf)
	ctx, cancel := context.WithCancel(u.ctx)
	u.partialCancel = cancel
	u.mu.Unlock()
	defer cancel()

	r, err := u.t.Transcribe(ctx, snap)
	if err != nil {
		return
	}
	select {
	case <-u.partials:
	default:
	}
	select {
	case u.partials <- r:
	default:
	}
}

// decodeFinal decodes everything held. Silence is not a request: an utterance
// that closed before any audio arrived is ordinary, and the endpoint answers
// 400 to an empty file.
func (u *Utterance) decodeFinal() outcome {
	u.mu.Lock()
	buf := u.buf
	u.mu.Unlock()
	if len(buf) == 0 {
		return outcome{}
	}
	r, err := u.t.Transcribe(u.ctx, buf)
	if err != nil {
		return outcome{err: fmt.Errorf("finish utterance: %w", err)}
	}
	return outcome{res: r}
}
