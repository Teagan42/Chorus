package session

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/teaganglenn/chorus/internal/journal"
)

// speechChannel is the Speaking child. One utterance plays at a time; the rest
// queue, because "one sec" -> tool result -> "found three" is the normal shape
// (SPEC §4.2).
type speechChannel struct {
	s *Session

	mu      sync.Mutex
	idle    *sync.Cond
	current *utterance
	pending []*utterance
	live    int

	// cut latches the interruption until the next turn. Without it, a delta
	// racing the barge-in starts playing speech nobody may hear.
	cut bool
}

// utterance is one speak call. Text arrives as deltas, so playback starts
// before the model has finished generating it (SPEC §4.1).
type utterance struct {
	callID string
	buf    []string
	stream Stream

	ctx    context.Context
	cancel context.CancelFunc

	// last closes when the final delta has been written, which is when
	// Stream.Close may wait for playback to drain.
	last     chan struct{}
	lastSeen bool
	started  bool

	// reason records why a cut happened, for the discard event.
	reason string
}

func newSpeechChannel(s *Session) *speechChannel {
	c := &speechChannel{s: s}
	c.idle = sync.NewCond(&c.mu)
	return c
}

// deliver applies the call's channel mode and streams the delta. It reports
// false once the channel has been cut, so the caller records the text as
// generated-but-never-heard instead.
func (c *speechChannel) deliver(d SpeechDelta) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.cut {
		return false
	}
	u := c.find(d.CallID)
	if u == nil {
		u = &utterance{callID: d.CallID, last: make(chan struct{})}
		c.live++
		switch mode(d.Mode) {
		case ModePreempt:
			// A tool result invalidated what was about to be said.
			c.cutLocked("preempted")
			c.dropLocked("preempted")
			c.pending = []*utterance{u}
		case ModeInterject:
			// Duck and cut in, but keep what was queued behind.
			c.cutLocked("preempted")
			c.pending = append([]*utterance{u}, c.pending...)
		default:
			c.pending = append(c.pending, u)
		}
	}

	if d.Text != "" {
		if u.started {
			c.s.fail(u.stream.Write(d.Text))
		} else {
			u.buf = append(u.buf, d.Text)
		}
	}
	if d.Last && !u.lastSeen {
		u.lastSeen = true
		if u.started {
			close(u.last)
		}
	}
	c.startNextLocked()
	return true
}

// resume reopens the channel for a new turn.
func (c *speechChannel) resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = false
}

// interrupt stops the playing utterance and discards the queue. Both halves
// are recorded; neither is silently dropped (SPEC §4.4).
func (c *speechChannel) interrupt(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = true
	c.cutLocked(reason)
	c.dropLocked(reason)
}

// cutLocked stops the playing utterance, tagging why for its record.
func (c *speechChannel) cutLocked(reason string) {
	if c.current == nil {
		return
	}
	c.current.reason = reason
	c.current.cancel()
}

// dropLocked discards queued utterances that were generated but never played.
func (c *speechChannel) dropLocked(reason string) {
	for _, u := range c.pending {
		if text := strings.Join(u.buf, ""); text != "" {
			c.s.fail(c.s.record(journal.Record{
				Kind: journal.KindSpeechDiscarded,
				Fields: map[string]string{
					"unspoken_text": text, "reason": reason,
				},
			}))
		}
		c.s.result(u.callID, "cancelled", "")
		c.live--
	}
	c.pending = nil
	c.idle.Broadcast()
}

func (c *speechChannel) find(callID string) *utterance {
	if c.current != nil && c.current.callID == callID {
		return c.current
	}
	for _, u := range c.pending {
		if u.callID == callID {
			return u
		}
	}
	return nil
}

// startNextLocked begins playback when the channel is free.
func (c *speechChannel) startNextLocked() {
	if c.current != nil || len(c.pending) == 0 {
		return
	}
	u := c.pending[0]
	c.pending = c.pending[1:]

	// Parent is the session, not the turn: queued speech may follow a tool
	// result that arrives after the turn's action stream closed.
	u.ctx, u.cancel = context.WithCancel(c.s.ctx)
	stream, err := c.s.sup.cfg.Speaker.Open(u.ctx, u.callID)
	if err != nil {
		c.s.fail(fmt.Errorf("open tts %s: %w", u.callID, err))
		c.s.result(u.callID, "error", `{"error":"tts_unavailable"}`)
		u.cancel()
		c.live--
		c.idle.Broadcast()
		return
	}
	u.stream, u.started = stream, true
	for _, text := range u.buf {
		c.s.fail(stream.Write(text))
	}
	u.buf = nil
	if u.lastSeen {
		close(u.last)
	}
	c.current = u
	c.s.enter("speaking")
	go c.play(u)
}

// play waits for generation to finish or for a cut, then records the truth.
func (c *speechChannel) play(u *utterance) {
	select {
	case <-u.last:
	case <-u.ctx.Done():
	}
	c.record(u, u.stream.Close())

	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.leave("speaking")
	c.current = nil
	c.live--
	c.startNextLocked()
	c.idle.Broadcast()
}

// record writes what the DAC actually played. The spoken half is kept and
// marked interrupted; the unspoken half is a different event (SPEC §4.4).
func (c *speechChannel) record(u *utterance, pb Playback) {
	callID, frames := u.callID, strconv.FormatInt(pb.Frames, 10)
	reason := u.reason
	if reason == "" {
		reason = "barge_in"
	}
	switch {
	case pb.Truncated && pb.Spoken == "":
		// Nothing was heard, so there is no split to record.
		if pb.Unspoken != "" {
			c.s.fail(c.s.record(journal.Record{
				Kind: journal.KindSpeechDiscarded,
				Fields: map[string]string{
					"unspoken_text": pb.Unspoken, "reason": reason,
				},
			}))
		}
		c.s.result(callID, "cancelled", "")
	case pb.Truncated:
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechTruncated, AudioRef: pb.AudioRef,
			Fields: map[string]string{
				"spoken_text": pb.Spoken, "unspoken_text": pb.Unspoken,
				"frames_played": frames,
			},
		}))
		c.s.result(callID, "cancelled", "")
	case pb.Spoken != "":
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechSpoken, AudioRef: pb.AudioRef,
			Fields: map[string]string{"text": pb.Spoken, "frames_played": frames},
		}))
		c.s.result(callID, "ok", "")
	default:
		c.s.result(callID, "ok", "")
	}
}

// waitIdle blocks until nothing is queued or playing.
func (c *speechChannel) waitIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.live > 0 {
		c.idle.Wait()
	}
}
