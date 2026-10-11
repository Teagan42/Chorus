package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/triage"
)

// logs keeps what the screens derive from each log, by the seq it was read
// to. The journal is append-only, so a log whose last seq has not moved
// derives the same again; one that grew is read and derived anew (SPEC §8).
type logs struct {
	mu   sync.Mutex
	byID map[string]*derived
}

// derived is everything the household's screens read of one log, never
// changed once made: requests share it.
type derived struct {
	seq uint64
	// start is when the log's first event was recorded; zero for an empty log.
	start time.Time

	scan    harvest.Result
	scanErr error

	signals, positives []triage.Signal
	triageErr          error

	// turns are a conversation's, as Replay compares them.
	turns    []rerun.Turn
	turnsErr error

	// summary is a conversation's, for Browse; device a satellite's own log.
	summary   convSummary
	device    *deviceLog
	negatives []harvest.Negative
	negErr    error
}

// of returns what id derives as of its last seq now, reading the log only
// when it has grown since it was last read.
func (l *logs) of(ctx context.Context, store journal.Store, id string) (*derived, error) {
	seq, err := store.LastSeq(ctx, id)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	d := l.byID[id]
	l.mu.Unlock()
	if d != nil && d.seq == seq {
		return d, nil
	}
	events, err := store.Events(ctx, id)
	if err != nil {
		return nil, err
	}
	d = derive(ctx, id, events)
	l.mu.Lock()
	defer l.mu.Unlock()
	// A request that read the log before it grew must not undo one that read it after.
	if cur := l.byID[id]; cur == nil || cur.seq < d.seq {
		if l.byID == nil {
			l.byID = map[string]*derived{}
		}
		l.byID[id] = d
	}
	return d, nil
}

// retain forgets every log the listing no longer names: retention deleted
// it, and what it derived must go with it (ADR-0062, ADR-0065).
func (l *logs) retain(ids []string) {
	listed := make(map[string]bool, len(ids))
	for _, id := range ids {
		listed[id] = true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	maps.DeleteFunc(l.byID, func(id string, _ *derived) bool { return !listed[id] })
}

func derive(ctx context.Context, id string, events []journal.Event) *derived {
	d := &derived{}
	if len(events) > 0 {
		d.seq, d.start = events[len(events)-1].Seq, events[0].At
	}
	read := frozen{id: id, events: events}
	d.scan, d.scanErr = harvest.Scan(ctx, read, id)
	d.signals, d.positives, d.triageErr = triage.ScanAll(ctx, read, id)
	switch {
	case id == houseLog:
	case strings.HasPrefix(id, devicePrefix):
		d.device = deviceOf(events)
		d.negatives, d.negErr = harvest.Negatives(ctx, read, id)
	default:
		d.turns, d.turnsErr = rerun.Turns(events)
		d.summary = summarize(id, events, d.signals)
	}
	return d
}

// frozen is one log as it was read, so every derivation sees the same
// events and the store is asked once for all of them.
type frozen struct {
	id     string
	events []journal.Event
}

func (f frozen) Events(_ context.Context, id string) ([]journal.Event, error) {
	if id != f.id {
		return nil, fmt.Errorf("events %s: only %s was read", id, f.id)
	}
	return f.events, nil
}

func (f frozen) LastSeq(ctx context.Context, id string) (uint64, error) {
	events, err := f.Events(ctx, id)
	if err != nil || len(events) == 0 {
		return 0, err
	}
	return events[len(events)-1].Seq, nil
}

func (f frozen) Append(context.Context, journal.Event) error {
	return errors.New("append: a log read for review is read-only")
}
