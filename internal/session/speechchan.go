package session

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
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

	// cut latches the interruption until the next turn, with its reason:
	// a barge-in, the session's end, or the voice failing. Without it, a
	// delta racing the barge-in starts playing speech nobody may hear.
	cut string

	// cannedSaid is set once this turn has said a canned line. One apology
	// is enough, and a voice that failed the first cannot say a second.
	cannedSaid bool

	// closed latches the session's end: nothing more plays, an announcement
	// included, which a cut alone does not stop.
	closed bool

	// asked is when this turn's ask ended, and heard whether its first frame
	// has been journalled. One start per turn (ADR-0035).
	asked time.Time
	heard bool

	// dead holds calls already cut or dropped, and why. A preempt does not
	// latch the channel, so a trailing delta would otherwise write to a
	// stream that is closing, or re-speak text already recorded as never
	// heard. Each has its result journalled, or about to be.
	dead map[string]string
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

	// canned marks a canned line, which a failure of its own does not
	// apologise for again (ADR-0051).
	canned bool

	// held marks an utterance an interjection paused, and after is that
	// interjection. Deltas still arriving gather in buf, for the rest.
	held  bool
	after *utterance

	// closed tells the watcher of a Starter that playback is over, and
	// watched closes once it has stopped watching. Nil when the stream
	// cannot see its DAC.
	closed  chan struct{}
	watched chan struct{}
}

func newSpeechChannel(s *Session) *speechChannel {
	c := &speechChannel{s: s, dead: map[string]string{}}
	c.idle = sync.NewCond(&c.mu)
	return c
}

// deliver applies the call's channel mode and streams the delta. It reports
// false once the channel has been cut, so the caller records the text as
// generated-but-never-heard instead.
func (c *speechChannel) deliver(d SpeechDelta) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cut != "" {
		return false
	}
	return c.deliverLocked(d, false, nil, false)
}

// refused says why speech for callID that arrives after a cut was not
// played, and whether the channel already resolved that call: a call cut or
// failed mid-stream has its result, and a trailing delta must not replace it.
func (c *speechChannel) refused(callID string) (reason string, resolved bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if reason, ok := c.dead[callID]; ok {
		return reason, true
	}
	return c.cutReasonLocked(), false
}

func (c *speechChannel) cutReasonLocked() string {
	if c.cut == "" {
		// Not cut: the turn was, by the barge-in that cancels it.
		return "barge_in"
	}
	return c.cut
}

// claimCanned spends this turn's canned line and names its speak call, or
// returns empty when one has been said or nothing more will play.
func (c *speechChannel) claimCanned() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.claimCannedLocked()
}

func (c *speechChannel) claimCannedLocked() string {
	if c.closed || c.cannedSaid {
		return ""
	}
	c.cannedSaid = true
	return "cn_" + newID()[:8]
}

// canned queues a canned line whole. It plays past a cut, as the voice's
// own failure cuts the turn; only the session's end refuses it.
func (c *speechChannel) canned(callID, text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cannedLocked(callID, text)
}

func (c *speechChannel) cannedLocked(callID, text string) bool {
	return c.deliverLocked(SpeechDelta{CallID: callID, Text: text, Mode: ModeQueue, Last: true}, false, nil, true)
}

// announce queues an announcement whole. A barge-in cut the turn, and this
// is no part of the turn; only the session's end refuses it.
func (c *speechChannel) announce(callID, text string, heard chan<- bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.deliverLocked(SpeechDelta{CallID: callID, Text: text, Mode: ModeQueue, Last: true}, true, heard, false)
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

func (c *speechChannel) deliverLocked(d SpeechDelta, announces bool, heard chan<- bool, canned bool) bool {
	if _, dead := c.dead[d.CallID]; c.closed || dead {
		return false
	}
	u := c.find(d.CallID)
	if u == nil {
		u = &utterance{callID: d.CallID, last: make(chan struct{}), announces: announces, heard: heard, canned: canned}
		c.live++
		switch mode(d.Mode) {
		case ModePreempt:
			// A tool result invalidated what was about to be said.
			c.cutLocked("preempted")
			c.dropLocked("preempted")
			c.pending = append([]*utterance{u}, c.pending...)
		case ModeInterject:
			// Pause what is playing and cut in, keeping it and the queue.
			c.holdLocked(u)
			c.pending = append([]*utterance{u}, c.pending...)
		default:
			c.pending = append(c.pending, u)
		}
	}

	if d.Text != "" {
		if u.started && !u.held {
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
	c.cut, c.cannedSaid = "", false
	c.dead = map[string]string{}
	c.asked, c.heard = asked, false
}

// interrupt stops the playing utterance and discards the queue. Both halves
// are recorded; neither is silently dropped (SPEC §4.4).
func (c *speechChannel) interrupt(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut = reason
	c.cutLocked(reason)
	c.dropLocked(reason)
}

// shut is the interruption the session's end makes, after which nothing
// plays again.
func (c *speechChannel) shut(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cut, c.closed = reason, true
	c.cutLocked(reason)
	c.dropLocked(reason)
}

// holdLocked pauses the playing utterance for the interjection u. play
// records what was heard and queues the rest behind u (ADR-0056).
func (c *speechChannel) holdLocked(u *utterance) {
	if c.current == nil || c.current.held {
		return
	}
	c.current.held, c.current.after = true, u
	c.current.reason = "interjected"
	c.current.cancel()
}

// cutLocked stops the playing utterance, tagging why for its record.
func (c *speechChannel) cutLocked(reason string) {
	if c.current == nil {
		return
	}
	c.current.reason = reason
	c.dead[c.current.callID] = reason
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
		c.dead[u.callID] = reason
		if text := strings.Join(u.buf, ""); text != "" {
			c.discardedLocked(u.callID, text, reason)
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
		// The voice failed before a word of this was heard: never played,
		// and never mistaken for a cut (ADR-0051).
		reason := ErrVoiceUnavailable.Error()
		c.dead[u.callID] = reason
		tell(u.heard, false)
		u.cancel()
		c.live--
		id := c.claimForLocked(u, reason)
		c.recordFailure(u.callID, reason, fmt.Errorf("open: %w", err), id)
		if text := strings.Join(u.buf, ""); text != "" {
			c.discardedLocked(u.callID, text, reason)
		}
		c.s.result(u.callID, "error", failedResult(reason))
		c.failedLocked(u, reason, id)
		c.idle.Broadcast()
		c.startNextLocked()
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
	// applied, which writes it. Cut short with no cut asked for is the voice
	// or the device failing, never the person (ADR-0051).
	c.mu.Lock()
	reason, id := u.reason, ""
	failed := reason == "" && pb.Truncated
	if failed {
		reason = failureReason(pb.Failure)
		id = c.claimForLocked(u, reason)
	}
	rest := pb.Unspoken + strings.Join(u.buf, "")
	paused := reason == "interjected" && (rest != "" || !u.lastSeen)
	if reason == "interjected" && !paused {
		// It ended as the interjection landed: nothing is left to resume.
		reason = ""
	}
	// Deltas a pause gathered, which a cut since then means nobody hears.
	gathered := ""
	if u.held && !paused {
		gathered = strings.Join(u.buf, "")
	}
	c.mu.Unlock()

	// Recorded before the next utterance may start, so the log order is the
	// order the queue was heard in, and the failure ahead of what it cut.
	if failed {
		c.recordFailure(u.callID, reason, pb.Failure, id)
	}
	if paused {
		c.recordPause(u.callID, rest, pb)
	} else {
		c.record(u.callID, reason, failed, pb)
		tell(u.heard, !pb.Truncated || pb.Spoken != "")
	}
	if gathered != "" {
		c.discardedLocked(u.callID, gathered, reason)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if failed {
		c.failedLocked(u, reason, id)
	}
	if paused {
		c.resumeLocked(u, pb)
	}
	c.current = nil
	c.live--
	c.startNextLocked()
	// After the next has started, so speech queued back to back is never
	// seen to stop between utterances.
	c.s.leave("speaking")
	c.idle.Broadcast()
}

// recordPause writes what was heard before an interjection paused a call.
// Its result waits for the rest, which is recorded as it plays (ADR-0056).
func (c *speechChannel) recordPause(callID, rest string, pb Playback) {
	frames := strconv.FormatInt(pb.Frames, 10)
	switch {
	case pb.Spoken == "":
		// Nothing was heard yet: all of it is the rest.
	case rest == "":
		// Heard to the end of what was generated; the model is still going.
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechSpoken, AudioRef: pb.AudioRef,
			Fields: map[string]string{"text": pb.Spoken, "frames_played": frames, "call_id": callID},
		}))
	default:
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechTruncated, AudioRef: pb.AudioRef,
			Fields: map[string]string{
				"spoken_text": pb.Spoken, "unspoken_text": rest,
				"frames_played": frames, "call_id": callID, "reason": "interjected",
			},
		}))
	}
}

// resumeLocked queues the rest of a paused call right behind the
// interjection that paused it. A cut that landed while the pause was being
// recorded means the rest is never heard, and is recorded so.
func (c *speechChannel) resumeLocked(u *utterance, pb Playback) {
	text := pb.Unspoken + strings.Join(u.buf, "")
	if u.reason != "interjected" {
		tell(u.heard, pb.Spoken != "")
		if text != "" {
			c.discardedLocked(u.callID, text, u.reason)
		}
		c.s.result(u.callID, "cancelled", "")
		return
	}
	rest := &utterance{
		callID: u.callID, last: make(chan struct{}), lastSeen: u.lastSeen,
		announces: u.announces, heard: u.heard, canned: u.canned,
	}
	if text != "" {
		rest.buf = []string{text}
	}
	c.live++
	at := slices.Index(c.pending, u.after) + 1
	c.pending = slices.Insert(c.pending, at, rest)
}

// record writes what the DAC actually played. The spoken half is kept and
// marked interrupted; the unspoken half is a different event (SPEC §4.4).
// Either names why it was cut, and a failed call's result says it failed.
func (c *speechChannel) record(callID, reason string, failed bool, pb Playback) {
	frames := strconv.FormatInt(pb.Frames, 10)
	outcome, result := "cancelled", ""
	if failed {
		outcome, result = "error", failedResult(reason)
	}
	switch {
	case pb.Truncated && pb.Spoken == "":
		// Nothing was heard, so there is no split to record.
		if pb.Unspoken != "" {
			c.discardedLocked(callID, pb.Unspoken, reason)
		}
		c.s.result(callID, outcome, result)
	case pb.Truncated:
		c.s.fail(c.s.record(journal.Record{
			Kind: journal.KindSpeechTruncated, AudioRef: pb.AudioRef,
			Fields: map[string]string{
				"spoken_text": pb.Spoken, "unspoken_text": pb.Unspoken,
				"frames_played": frames, "call_id": callID, "reason": reason,
			},
		}))
		c.s.result(callID, outcome, result)
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

// failureReason names a failed playback for the journal. One the stream
// did not explain is still the voice's: nobody cut it.
func failureReason(err error) string {
	if errors.Is(err, ErrPlaybackUnconfirmed) {
		return ErrPlaybackUnconfirmed.Error()
	}
	return ErrVoiceUnavailable.Error()
}

// failedResult is a failed speak call's result, as an Open that failed
// has always reported it.
func failedResult(reason string) string { return `{"error":"` + reason + `"}` }

// claimForLocked spends the turn's canned line on a voice failure, unless
// the failure was an announcement's or the canned line's own.
func (c *speechChannel) claimForLocked(u *utterance, reason string) string {
	if reason != ErrVoiceUnavailable.Error() || u.announces || u.canned {
		return ""
	}
	return c.claimCannedLocked()
}

// recordFailure journals why a speak call's audio failed. Safe with or
// without the channel's lock: it touches only the journal.
func (c *speechChannel) recordFailure(callID, reason string, err error, cannedID string) {
	fields := map[string]string{"call_id": callID, "reason": reason}
	if err != nil {
		fields["error"] = err.Error()
	}
	if cannedID != "" {
		fields["canned_call_id"] = cannedID
	}
	c.s.sup.cfg.Log.Warn("speech failed", "conversation", c.s.convID, "call", callID, "reason", reason, "err", err)
	c.s.fail(c.s.record(journal.Record{Kind: journal.KindSpeechFailed, Fields: fields}))
}

// failedLocked ends the turn's speech once the voice has failed: what is
// queued behind would fail the same way, and gaps where words went missing
// say something nobody meant. The canned line plays past the cut. An
// announcement is no part of the turn, and a device that stopped reporting
// may still be playing, so neither cuts it.
func (c *speechChannel) failedLocked(u *utterance, reason, cannedID string) {
	if u.announces || reason != ErrVoiceUnavailable.Error() {
		return
	}
	if c.cut == "" {
		c.cut = reason
	}
	c.dropLocked(reason)
	if cannedID != "" {
		c.s.sayCannedLocked(cannedID, c.s.sup.cfg.Canned.Voice)
	}
}

// discardedLocked journals speech nobody heard. Safe with or without the
// channel's lock: it touches only the journal.
func (c *speechChannel) discardedLocked(callID, text, reason string) {
	c.s.fail(c.s.record(journal.Record{
		Kind:   journal.KindSpeechDiscarded,
		Fields: map[string]string{"unspoken_text": text, "reason": reason, "call_id": callID},
	}))
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
