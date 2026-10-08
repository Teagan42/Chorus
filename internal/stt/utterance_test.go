package stt_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/stt"
)

// patience bounds a wait that should already be over. It is a failure-path
// deadline only; nothing here sleeps or reads a clock to make progress.
const patience = 5 * time.Second

// halfSecond is DefaultPartialEvery in bytes: 16 kHz, 2 bytes a sample.
const halfSecond = 16000

// call is one Transcribe the fake received. The test answers it through reply,
// which is what makes a slow decode scriptable: the decode is slow for
// exactly as long as the test holds the reply back.
type call struct {
	ctx   context.Context
	pcm   []byte
	reply chan outcome
}

type outcome struct {
	res stt.Result
	err error
}

// fake is a scripted Transcriber. Every decode is parked until the test
// answers it or its context ends, so cadence and coalescing are observed
// directly rather than inferred from timing.
type fake struct {
	calls chan call
}

func newFake() *fake { return &fake{calls: make(chan call, 16)} }

func (f *fake) Transcribe(ctx context.Context, pcm []byte) (stt.Result, error) {
	c := call{ctx: ctx, pcm: pcm, reply: make(chan outcome, 1)}
	f.calls <- c
	select {
	case o := <-c.reply:
		return o.res, o.err
	case <-ctx.Done():
		return stt.Result{}, ctx.Err()
	}
}

// next waits for the fake's next decode.
func (f *fake) next(t *testing.T) call {
	t.Helper()
	select {
	case c := <-f.calls:
		return c
	case <-time.After(patience):
		t.Fatal("no decode arrived")
		return call{}
	}
}

// none asserts no decode is pending. Deterministic rather than a sleep: by the
// time a later decode has been observed, any earlier one would already be on
// the channel.
func (f *fake) none(t *testing.T) {
	t.Helper()
	select {
	case c := <-f.calls:
		t.Fatalf("unexpected decode of %d bytes", len(c.pcm))
	default:
	}
}

func (c call) answer(text string) { c.reply <- outcome{res: stt.Result{Text: text}} }

func (c call) fail(err error) { c.reply <- outcome{err: err} }

func audio(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i)
	}
	return out
}

// open starts an utterance and makes the test wait for its goroutine, so a
// leaked decoder fails the test rather than outliving it.
func open(t *testing.T, f *fake, opts stt.Options) (*stt.Utterance, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	u, err := stt.Open(ctx, f, opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		select {
		case <-u.Done():
		case <-time.After(patience):
			t.Error("the decode goroutine outlived its context")
		}
	})
	return u, cancel
}

func write(t *testing.T, u *stt.Utterance, pcm []byte) {
	t.Helper()
	if err := u.Write(pcm); err != nil {
		t.Fatalf("write %d bytes: %v", len(pcm), err)
	}
}

func partial(t *testing.T, u *stt.Utterance) stt.Result {
	t.Helper()
	select {
	case r := <-u.Partials():
		return r
	case <-time.After(patience):
		t.Fatal("no partial arrived")
		return stt.Result{}
	}
}

// finish runs Finish off the test goroutine so the test can answer the final
// decode it provokes.
func finish(u *stt.Utterance) <-chan outcome {
	out := make(chan outcome, 1)
	go func() {
		r, err := u.Finish(context.Background())
		out <- outcome{r, err}
	}()
	return out
}

func finished(t *testing.T, out <-chan outcome) outcome {
	t.Helper()
	select {
	case o := <-out:
		return o
	case <-time.After(patience):
		t.Fatal("Finish did not return")
		return outcome{}
	}
}

// A partial is re-decoded from the whole buffer every half second of audio,
// counted in bytes: one byte short of the cadence decodes nothing.
//
// verifies SPEC §4.3, §4.5
func TestPartialsFollowTheAudio(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond-2))
	f.none(t)
	write(t, u, audio(2))
	c := f.next(t)
	if len(c.pcm) != halfSecond {
		t.Fatalf("first decode saw %d bytes, want %d", len(c.pcm), halfSecond)
	}
	c.answer("turn off")
	if got := partial(t, u); got.Text != "turn off" {
		t.Errorf("partial = %q", got.Text)
	}

	write(t, u, audio(halfSecond))
	c = f.next(t)
	// The whole utterance so far, not the new half second: a batch endpoint
	// has no state to continue from.
	if len(c.pcm) != 2*halfSecond {
		t.Fatalf("second decode saw %d bytes, want %d", len(c.pcm), 2*halfSecond)
	}
	c.answer("turn off the kitchen")
	if got := partial(t, u); got.Text != "turn off the kitchen" {
		t.Errorf("partial = %q", got.Text)
	}

	done := finish(u)
	c = f.next(t)
	if len(c.pcm) != 2*halfSecond {
		t.Fatalf("final decode saw %d bytes, want %d", len(c.pcm), 2*halfSecond)
	}
	c.answer("turn off the kitchen lights")
	if o := finished(t, done); o.err != nil || o.res.Text != "turn off the kitchen lights" {
		t.Errorf("final = %+v", o)
	}
}

// Audio that lands while a decode is in flight collapses into one re-decode
// of everything, not one per cadence: each decode reads the whole buffer, so
// a queue could only fall further behind the speaker.
//
// verifies SPEC §11
func TestASlowDecodeCoalescesToTheLatestAudio(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond))
	slow := f.next(t)
	for range 3 {
		write(t, u, audio(halfSecond))
	}
	f.none(t)
	slow.answer("turn")

	c := f.next(t)
	if len(c.pcm) != 4*halfSecond {
		t.Fatalf("the re-decode saw %d bytes, want all %d", len(c.pcm), 4*halfSecond)
	}
	c.answer("turn off the kitchen")
	done := finish(u)
	// The next decode is the final. A queued third partial would sit here
	// instead and Finish would still be waiting.
	f.next(t).answer("turn off the kitchen lights")
	if o := finished(t, done); o.err != nil || o.res.Text != "turn off the kitchen lights" {
		t.Errorf("final = %+v", o)
	}
	f.none(t)
}

// A partial that fails is dropped: the next decode covers the same audio.
//
// verifies SPEC §4.3
func TestAFailedPartialDoesNotEndTheUtterance(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond))
	f.next(t).fail(errors.New("transcribe: 503 Service Unavailable"))
	write(t, u, audio(halfSecond))
	f.next(t).answer("turn off")
	if got := partial(t, u); got.Text != "turn off" {
		t.Errorf("partial after a failure = %q", got.Text)
	}

	done := finish(u)
	f.next(t).answer("turn off the lights")
	if o := finished(t, done); o.err != nil || o.res.Text != "turn off the lights" {
		t.Errorf("final = %+v", o)
	}
}

// Nothing follows the final, so its failure is the utterance's.
func TestAFailedFinalIsAnError(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(2))
	done := finish(u)
	boom := errors.New("transcribe: 500 Internal Server Error")
	f.next(t).fail(boom)
	if o := finished(t, done); !errors.Is(o.err, boom) {
		t.Errorf("err = %v, want it to wrap the decode failure", o.err)
	}
}

// Finish aborts a partial in flight rather than waiting behind it: the final
// replaces that partial's text, and the turn cannot start until the final
// lands (SPEC §11).
//
// verifies SPEC §11
func TestFinishAbandonsThePartialInFlight(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond))
	slow := f.next(t)
	write(t, u, audio(2))
	done := finish(u)

	select {
	case <-slow.ctx.Done():
	case <-time.After(patience):
		t.Fatal("the partial in flight was not cancelled")
	}
	c := f.next(t)
	if len(c.pcm) != halfSecond+2 {
		t.Errorf("final decode saw %d bytes, want %d", len(c.pcm), halfSecond+2)
	}
	c.answer("done")
	if o := finished(t, done); o.err != nil || o.res.Text != "done" {
		t.Errorf("final = %+v", o)
	}
}

// Only the newest partial is worth reading. Two unread partials yield the
// later one, not a backlog the gate has to drain.
//
// verifies SPEC §4.3
func TestTheLatestPartialReplacesAnUnreadOne(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond))
	f.next(t).answer("turn")
	write(t, u, audio(halfSecond))
	f.next(t).answer("turn off")
	// A third decode arriving proves the second partial has been published.
	write(t, u, audio(halfSecond))
	third := f.next(t)

	if got := partial(t, u); got.Text != "turn off" {
		t.Errorf("partial = %q, want the latest", got.Text)
	}
	select {
	case r := <-u.Partials():
		t.Errorf("a stale partial %q was still queued", r.Text)
	default:
	}
	third.answer("turn off the")
}

// A session that ended no longer wants the turn: Finish fails cleanly instead
// of returning its last partial, and the goroutine is gone.
//
// verifies SPEC §4
func TestACancelledUtteranceFailsFinishCleanly(t *testing.T) {
	f := newFake()
	u, cancel := open(t, f, stt.Options{})

	write(t, u, audio(halfSecond))
	slow := f.next(t)
	cancel()
	select {
	case <-slow.ctx.Done():
	case <-time.After(patience):
		t.Fatal("cancelling the utterance did not cancel its decode")
	}
	select {
	case <-u.Done():
	case <-time.After(patience):
		t.Fatal("the decode goroutine did not exit on cancel")
	}

	r, err := u.Finish(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if r.Text != "" {
		t.Errorf("a cancelled utterance returned %q", r.Text)
	}
	f.none(t)
}

// Finish's own context bounds the wait for a sidecar that never answers.
func TestFinishHonoursItsOwnDeadline(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})
	write(t, u, audio(2))

	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan outcome, 1)
	go func() {
		r, err := u.Finish(ctx)
		out <- outcome{r, err}
	}()
	final := f.next(t)
	cancel()
	if o := finished(t, out); !errors.Is(o.err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", o.err)
	}
	// The decode itself is still bound to the utterance, not to Finish.
	if final.ctx.Err() != nil {
		t.Error("Finish's deadline cancelled the utterance's own decode")
	}
	final.answer("late")
}

// Audio past the bound is refused, and what is held is still decoded: a
// stuck mic must not grow a buffer the re-decode is quadratic in.
//
// verifies SPEC §4.5
func TestAnUtteranceIsBounded(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{MaxBytes: 2 * halfSecond})

	write(t, u, audio(2*halfSecond))
	f.next(t).answer("one two")
	if err := u.Write(audio(2)); !errors.Is(err, stt.ErrTooLong) {
		t.Fatalf("err = %v, want ErrTooLong", err)
	}

	done := finish(u)
	c := f.next(t)
	if len(c.pcm) != 2*halfSecond {
		t.Errorf("final decode saw %d bytes, want the bounded %d", len(c.pcm), 2*halfSecond)
	}
	c.answer("one two")
	if o := finished(t, done); o.err != nil {
		t.Errorf("final: %v", o.err)
	}
}

// Silence is not a request. The endpoint answers 400 to an empty file, and an
// utterance that closed before any audio arrived is ordinary.
func TestAnEmptyUtteranceMakesNoRequest(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})
	r, err := u.Finish(context.Background())
	if err != nil || r.Text != "" {
		t.Errorf("finish = %+v, %v", r, err)
	}
	f.none(t)
}

func TestWritesAreRefusedAfterFinishAndWhenTorn(t *testing.T) {
	f := newFake()
	u, _ := open(t, f, stt.Options{})

	if err := u.Write(audio(3)); err == nil {
		t.Error("an odd byte count must be refused")
	}
	done := finish(u)
	if o := finished(t, done); o.err != nil {
		t.Fatalf("finish: %v", o.err)
	}
	if err := u.Write(audio(2)); err == nil {
		t.Error("a write after finish must be refused")
	}
	if _, err := u.Finish(context.Background()); err == nil {
		t.Error("a second finish must be refused")
	}
}

func TestOpenRequiresATranscriber(t *testing.T) {
	if _, err := stt.Open(context.Background(), nil, stt.Options{}); err == nil {
		t.Fatal("an utterance with no transcriber must not open")
	}
}
