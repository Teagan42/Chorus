package session

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

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

	// closed latches the session's end: nothing more plays, an announcement
	// included, which a cut alone does not stop.
	closed bool

	// asked is when this turn's ask ended, and heard whether its first frame
	// has been journalled. One start per turn (ADR-0035).
	asked time.Time
	heard bool

	// dead holds calls already cut or dropped. A preempt does not latch the
	// channel, so a trailing delta would otherwise write to a stream that is
	// closing, or re-speak text already recorded as never heard.
	dead map[string]bool
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

	// announces marks an announcement, answering no ask, so its first frame
	// is not the turn's (ADR-0035). heard is told whether it was heard.
	announces bool
	heard     chan<- bool

	// closed tells the watcher of a Starter that playback is over, and
	// watched closes once it has stopped watching. Nil when the stream
	// cannot see its DAC.
	closed  chan struct{}
	watched chan struct{}
}

func newSpeechChannel(s *Session) *speechChannel {
	c := &speechChannel{s: s, dead: map[string]bool{}}
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
	return c.deliverLocked(d, false, nil)
}

// announce queues an announcement whole. A barge-in cut the turn, and this
// is no part of the turn; only the session's end refuses it.
func (c *speechChannel) announce(callID, text string, heard chan<- bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deliverLocked(SpeechDelta{CallID: callID, Text: text, Mode: ModeQueue, Last: true}, true, heard)
}

// tell reports whether an announcement was heard, once, never blocking.
func tell(heard chan<- bool, ok bool) {
	if heard == nil {
		return
	}
	select {
	case heard <- ok:
	default:
	}
}

func (c *speechChannel) deliverLocked(d SpeechDelta, announces bool, heard chan<- bool) bool {
	if c.closed || c.dead[d.CallID] {
		return false
	}
	u := c.find(d.CallID)
	if u == nil {
		u = &utterance{callID: d.CallID, last: make(chan struct{}), announces: announces, heard: heard}
		c.live++
		switch mode(d.Mode) {
		case ModePreempt:
			// A tool result invalidated what was about to be said.
			c.cutLocked("preempted")
			c.dropLocked("preempted")
			c.pending = append([]*utterance{u}, c.pending...)
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

// resume reopens the channel for a new turn, whose ask ended at asked.
func (c *speechChannel) resume(asked time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = false
	c.dead = map[string]bool{}
	c.asked, c.heard = asked, false
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

// shut is the interruption the session's end makes, after which nothing
// plays again.
func (c *speechChannel) shut(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut, c.closed = true, true
	c.cutLocked(reason)
	c.dropLocked(reason)
}

// cutLocked stops the playing utterance, tagging why for its record.
func (c *speechChannel) cutLocked(reason string) {
	if c.current == nil {
		return
	}
	c.current.reason = reason
	c.dead[c.current.callID] = true
	c.current.cancel()
}

// dropLocked discards queued utterances that were generated but never
// played. Announcements are no part of the turn, so stay queued until the
// session ends.
func (c *speechChannel) dropLocked(reason string) {
	var kept []*utterance
	for _, u := range c.pending {
		if u.announces && !c.closed {
			kept = append(kept, u)
			continue
		}
		tell(u.heard, false)
		c.dead[u.callID] = true
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
	c.pending = kept
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
		tell(u.heard, false)
		u.cancel()
		c.live--
		c.idle.Broadcast()
		return
	}
	// Register the child before the first write: the write is observable, so
	// anything that sees it must already see the child.
	c.s.enter("speaking")
	u.stream, u.started = stream, true
	for _, text := range u.buf {
		c.s.fail(stream.Write(text))
	}
	u.buf = nil
	if u.lastSeen {
		close(u.last)
	}
	c.current = u
	if st, ok := stream.(Starter); ok && !u.announces {
		u.closed, u.watched = make(chan struct{}), make(chan struct{})
		go c.watch(u, st.Started())
	}
	go c.play(u)
}

// watch journals the turn's first frame when this utterance is the one the
// household hears first (ADR-0035). It is done before play records the
// utterance, so the log reads the start before the speech it began.
func (c *speechChannel) watch(u *utterance, started <-chan struct{}) {
	defer close(u.watched)
	select {
	case <-started:
	case <-u.closed:
		// Both may be ready when a short utterance drains at once.
		select {
		case <-started:
		default:
			return
		}
	}
	c.mu.Lock()
	first, asked := !c.heard, c.asked
	c.heard = true
	c.mu.Unlock()
	if !first {
		return
	}
	fields := map[string]string{"call_id": u.callID}
	if !asked.IsZero() {
		wait := c.s.sup.cfg.Clock.Now().Sub(asked)
		fields["wait_ms"] = strconv.FormatInt(wait.Milliseconds(), 10)
	}
	c.s.fail(c.s.record(journal.Record{Kind: journal.KindSpeechStarted, Fields: fields}))
}

// play waits for generation to finish or for a cut, then records the truth.
func (c *speechChannel) play(u *utterance) {
	select {
	case <-u.last:
	case <-u.ctx.Done():
	}
	pb := u.stream.Close()
	if u.watched != nil {
		close(u.closed)
		<-u.watched
	}

	// Read the reason under the mutex: ending naturally races a cut being
	// applied, which writes it.
	c.mu.Lock()
	reason := u.reason
	c.mu.Unlock()

	// Recorded before the next utterance may start, so the log order is the
	// order the queue was heard in.
	c.record(u.callID, reason, pb)
	tell(u.heard, !pb.Truncated || pb.Spoken != "")

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
func (c *speechChannel) record(callID, reason string, pb Playback) {
	frames := strconv.FormatInt(pb.Frames, 10)
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
				"frames_played": frames, "call_id": callID,
			},
		}))
		c.s.result(callID, "cancelled", "")
	case pb.Spoken != "":
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechSpoken, AudioRef: pb.AudioRef,
			Fields: map[string]string{"text": pb.Spoken, "frames_played": frames, "call_id": callID},
		}))
		c.s.result(callID, "ok", "")
	default:
		c.s.result(callID, "ok", "")
	}
}

// busy reports whether anything is queued or playing.
func (c *speechChannel) busy() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.live > 0
}

// waitIdle blocks until nothing is queued or playing.
func (c *speechChannel) waitIdle() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for c.live > 0 {
		c.idle.Wait()
	}
}
