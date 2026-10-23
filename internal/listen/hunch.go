package listen

import (
	"context"
	"slices"

	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

// hunch is what a pause supposed an utterance said: its words, decoded from
// the audio so far, its speaker, and the turn asked from them (ADR-0095).
// Each field is written by guess before done closes.
type hunch struct {
	pause  uint64
	cancel context.CancelFunc
	done   chan struct{}

	res  stt.Result
	out  identity.Outcome
	err  error
	spec *session.Speculation
}

// pausedLocked follows the endpointer's pauses on the open utterance: a
// pause long enough is guessed at, and speech after it drops the guess.
func (l *Listener) pausedLocked() {
	p, ok := l.cfg.Endpointer.(Pauser)
	if !ok || !l.cfg.Speculate || l.cur == nil {
		return
	}
	u := l.cur
	n, paused := p.Paused()
	if u.hunch != nil && (!paused || u.hunch.pause != n) {
		l.dropLocked(u.hunch, session.DiscardResumed)
		u.hunch = nil
	}
	if !paused || u.hunch != nil || u.guessed == n+1 {
		return
	}
	// Each pause is considered once, whether or not it is guessed at.
	u.guessed = n + 1
	sess := l.sess
	// Only a session at rest can keep the guess: the wake's own utterance
	// opens it, and one still answering would answer differently.
	if u.first || u.overhearing || !l.liveLocked() || !slices.Equal(sess.Children(), []string{"listening"}) {
		return
	}
	pcm, _ := u.snapshot()
	ctx, cancel := context.WithCancel(l.ctx)
	h := &hunch{pause: n, cancel: cancel, done: make(chan struct{})}
	if !l.spawnLocked(func() { l.guess(ctx, h, sess, pcm) }) {
		cancel()
		return
	}
	u.hunch = h
}

// guess decodes the pause's audio, attributes it, and asks the session to
// answer it ahead of the turn.
func (l *Listener) guess(ctx context.Context, h *hunch, sess *session.Session, pcm []byte) {
	defer close(h.done)
	h.res, h.err = l.cfg.Transcriber.Transcribe(ctx, pcm)
	if h.err == nil {
		h.err = ctx.Err()
	}
	if h.err != nil {
		return
	}
	h.out = l.resolve(pcm)
	if h.res.Text == "" || (l.cfg.MinSustained >= 0 && Impulsive(pcm, l.cfg.SpeechEnergy, l.cfg.MinSustained)) {
		return
	}
	out := unjudged(h.out, pcm, sess.Speaker())
	h.spec = sess.Speculate(ctx, session.Transcript{
		Text: h.res.Text, SpeakerID: out.PersonID, SpeakerMatch: string(out.Reason), Embedding: out.Embedding,
	})
}

// dropLocked abandons a guess: its decode is cancelled, and its ask is
// discarded once it has one.
func (l *Listener) dropLocked(h *hunch, reason string) {
	h.cancel()
	l.spawnLocked(func() {
		<-h.done
		h.spec.Discard(reason)
	})
}

// settled is the guess the ended utterance's pause made, once it has
// finished, or nil when there was none or the link closed first.
func (l *Listener) settled(u *utterance) *hunch {
	if u.hunch == nil {
		return nil
	}
	select {
	case <-u.hunch.done:
		return u.hunch
	case <-l.ctx.Done():
		return nil
	}
}
