package retention

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// DefaultResume is how long after a conversation last did something the
// same person may wake again and resume it (session.MigrationWindow).
const DefaultResume = 2 * time.Minute

// Why an Eraser refuses. Each wraps into an error naming the log.
var (
	ErrNoLog     = errors.New("no such log")
	ErrLive      = errors.New("a live session's log")
	ErrResumable = errors.New("a conversation its person may still resume")
	ErrLongLived = errors.New("a satellite's or the house's own log, which chorusd writes all day")
	ErrNoEvent   = errors.New("no such event")
	ErrNoAudio   = errors.New("no audio kept for that event")
)

// Summaries forgets what a conversation was summarized as for the people
// in it (memory.Summary).
type Summaries interface {
	ForgetSummaries(ctx context.Context, conversationID string) error
}

// Eraser deletes on a person's word what a pass deletes on a horizon, by
// the same path, so the two cannot leave a log in different states
// (ADR-0065, ADR-0070).
type Eraser struct {
	// Journal writes audio_dropped.
	Journal  *journal.Journal
	Store    Store
	Blobs    Blobs
	Curation Curation
	// NotKept and Summaries are nil when nothing guards the disk or keeps
	// summaries.
	NotKept   NotKept
	Summaries Summaries
	Clock     journal.Clock

	// Live defaults to DefaultLive, Resume to DefaultResume.
	Live, Resume time.Duration
}

// Erased is what one deletion removed, counted, never what it held.
type Erased struct {
	Events, Clips int
	// Curated is a conversation a reviewer had kept work of.
	Curated bool
}

// Live reports whether a conversation's log belongs to a live session: it
// leaves a session open and did something within live (ADR-0022).
func Live(events []journal.Event, now time.Time, live time.Duration) bool {
	last, open := activity(events)
	return open && now.Sub(last) < live
}

// DeleteConversation removes a conversation whole: its audio, its curation
// rows, its summaries, then its log.
func (e *Eraser) DeleteConversation(ctx context.Context, id string) (Erased, error) {
	events, err := e.settled(ctx, id)
	if err != nil {
		return Erased{}, fmt.Errorf("delete %s: %w", id, err)
	}
	curated, err := curation.Curated(ctx, e.Curation, id)
	if err != nil {
		return Erased{}, fmt.Errorf("delete %s: %w", id, err)
	}
	clips, err := e.deleteLog(ctx, id, events)
	if err != nil {
		return Erased{}, fmt.Errorf("delete %s: %w", id, err)
	}
	return Erased{Events: len(events), Clips: clips, Curated: curated}, nil
}

// DropAudio removes every clip one event names that is still kept, and
// records each as redacted in the log, as retention records its own.
func (e *Eraser) DropAudio(ctx context.Context, id string, seq uint64) ([]string, error) {
	events, err := e.Store.Events(ctx, id)
	if err == nil {
		err = e.RefusesDrop(id, events)
	}
	if err != nil {
		return nil, fmt.Errorf("drop audio of %s #%d: %w", id, seq, err)
	}
	i := slices.IndexFunc(events, func(ev journal.Event) bool { return ev.Seq == seq })
	if i < 0 {
		return nil, fmt.Errorf("drop audio of %s #%d: %w", id, seq, ErrNoEvent)
	}
	gone := dropped(events)
	var refs []string
	for _, ref := range refsOf(events[i]) {
		if !gone[ref] {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil, fmt.Errorf("drop audio of %s #%d: %w", id, seq, ErrNoAudio)
	}
	for _, ref := range refs {
		if err := e.Blobs.Remove(ctx, ref); err != nil {
			return nil, fmt.Errorf("drop %s: %w", ref, err)
		}
		if err := e.drop(ctx, id, ref, "redacted", 0); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

// settled reads a conversation's log, refusing one chorusd may yet write.
func (e *Eraser) settled(ctx context.Context, id string) ([]journal.Event, error) {
	if longLived(id) {
		return nil, ErrLongLived
	}
	events, err := e.Store.Events(ctx, id)
	if err != nil {
		return nil, err
	}
	return events, e.Refuses(id, events)
}

// Refuses says why a log may not be erased now, or nil: a satellite's or
// the house's own, a live session's, one its person could still walk back
// into, or one with nothing in it.
func (e *Eraser) Refuses(id string, events []journal.Event) error {
	if longLived(id) {
		return ErrLongLived
	}
	if len(events) == 0 {
		return ErrNoLog
	}
	now := e.Clock.Now()
	if Live(events, now, cmp.Or(e.Live, DefaultLive)) {
		return ErrLive
	}
	if last, _ := activity(events); now.Sub(last) < cmp.Or(e.Resume, DefaultResume) {
		return ErrResumable
	}
	return nil
}

// RefusesDrop says why no clip may be dropped from a log now, or nil. A
// satellite's own log may always be: no session holds it, and the record
// goes after chorusd's next write, never in its place (journal.ErrSeqTaken).
func (e *Eraser) RefusesDrop(id string, events []journal.Event) error {
	switch {
	case id == journal.HouseTimers:
		return ErrLongLived
	case strings.HasPrefix(id, devicePrefix) && len(events) == 0:
		return ErrNoLog
	case strings.HasPrefix(id, devicePrefix):
		return nil
	}
	return e.Refuses(id, events)
}

// deleteLog removes a conversation's audio, its curation rows, its
// summaries and then its log, in that order: the log is what a failed
// attempt finds it again by. It returns how many clips it removed.
func (e *Eraser) deleteLog(ctx context.Context, id string, events []journal.Event) (int, error) {
	gone := dropped(events)
	n := 0
	for _, c := range clips(events) {
		if gone[c.ref] {
			continue
		}
		if err := e.Blobs.Remove(ctx, c.ref); err != nil {
			return n, err
		}
		n++
		if e.NotKept != nil {
			e.NotKept.Settle(c.ref)
		}
	}
	if err := e.Curation.Forget(ctx, id); err != nil {
		return n, err
	}
	if e.Summaries != nil {
		if err := e.Summaries.ForgetSummaries(ctx, id); err != nil {
			return n, err
		}
	}
	return n, e.Store.DeleteLog(ctx, id)
}

// drop records that a clip is not kept, in the log that names it.
func (e *Eraser) drop(ctx context.Context, id, ref, reason string, horizon time.Duration) error {
	fields := map[string]string{"audio_ref": ref, "reason": reason}
	if horizon > 0 {
		fields["days"] = strconv.Itoa(int(horizon / (24 * time.Hour)))
	}
	_, err := e.Journal.Append(ctx, id, journal.Record{Kind: journal.KindAudioDropped, Fields: fields})
	return err
}

// longLived is a log that is never deleted whole, only trimmed.
func longLived(id string) bool {
	return id == journal.HouseTimers || strings.HasPrefix(id, devicePrefix)
}
