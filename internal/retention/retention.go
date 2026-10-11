// Package retention removes what a household's policy no longer keeps:
// audio past its satellite's horizon, conversations past their journal
// horizon, and the old head of the long-lived device and house logs
// (SPEC §8, ADR-0065). What a reviewer curated is kept unless the house
// says otherwise, and a live session's conversation is never touched.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// DefaultEvery is how often a pass runs after the one at startup.
const DefaultEvery = time.Hour

// DefaultLive is how long a log left open may be silent and still be a live
// session's. Sessions do not survive a restart (SPEC §14), and a live one
// writes within minutes, so one silent for a day was left by a crash.
const DefaultLive = 24 * time.Hour

// recheckCurated is how soon a log kept for a reviewer is looked at again,
// in case the verdict that kept it was undone.
const recheckCurated = 24 * time.Hour

// devicePrefix names a satellite's own log (listen.DeviceConversation).
const devicePrefix = "device:"

// Horizons are how long one satellite's audio and logs are kept. Zero keeps
// forever.
type Horizons struct {
	Audio, Journal time.Duration
}

// Policy is the household's retention.
type Policy struct {
	House      Horizons
	Satellites map[string]Horizons

	// PruneCurated lets what a reviewer kept go with everything else.
	PruneCurated bool
}

// FromConfig is the inventory's policy, each satellite's horizons already
// falling back to the house's.
func FromConfig(c *config.Config) Policy {
	p := Policy{
		House:        Horizons{c.Retention.Audio.Duration(), c.Retention.Journal.Duration()},
		Satellites:   map[string]Horizons{},
		PruneCurated: c.Retention.PruneCurated,
	}
	for _, s := range c.Satellites {
		r := c.RetentionFor(s.Name)
		p.Satellites[s.Name] = Horizons{r.Audio.Duration(), r.Journal.Duration()}
	}
	return p
}

// For is a satellite's horizons. One no longer in the inventory, or a log
// that names none, has the house's.
func (p Policy) For(satellite string) Horizons {
	if h, ok := p.Satellites[satellite]; ok {
		return h
	}
	return p.House
}

// prunes reports whether any horizon is set: with none, a pass only
// journals what the disk guard declined.
func (p Policy) prunes() bool {
	if p.House != (Horizons{}) {
		return true
	}
	for _, h := range p.Satellites {
		if h != (Horizons{}) {
			return true
		}
	}
	return false
}

// Store is the journal as retention needs it: listed, read and deleted.
type Store interface {
	journal.Store
	journal.Lister
	journal.Deleter
}

// Curation is what a reviewer decided, and the forgetting of it once the
// log it names is gone.
type Curation interface {
	curation.Store
	curation.Forgetter
}

// Blobs removes audio. Removing one already gone is no error.
type Blobs interface {
	Remove(ctx context.Context, ref string) error
}

// NotKept is the disk guard's record of the audio it did not write
// (blob.Guarded).
type NotKept interface {
	Declined() map[string]time.Time
	Settle(ref string)
}

// Timers is the injected wait between passes.
type Timers interface {
	After(d time.Duration) <-chan time.Time
}

// Config wires a Pruner. Everything that reads a clock or does I/O is
// injected (CONTRIBUTING §1).
type Config struct {
	// Journal writes audio_dropped. It must be the daemon's own, whose
	// per-log lock orders these writes with the session's.
	Journal  *journal.Journal
	Store    Store
	Blobs    Blobs
	Curation Curation
	// NotKept is nil when nothing guards the disk.
	NotKept NotKept
	Clock   journal.Clock
	Timers  Timers
	Policy  Policy

	// Every defaults to DefaultEvery, Live to DefaultLive.
	Every time.Duration
	Live  time.Duration

	// Log hears each pass that removed something, and each failure. Nil
	// discards.
	Log *slog.Logger
}

// Report is what one pass removed.
type Report struct {
	// AudioPruned is clips removed past their audio horizon; AudioNotKept
	// the clips the disk guard declined, now journalled.
	AudioPruned, AudioNotKept int
	// LogsDeleted is conversations deleted whole; EventsDeleted the events
	// trimmed from the head of a device or the house log.
	LogsDeleted, EventsDeleted int
}

func (r Report) empty() bool { return r == Report{} }

// Pruner runs passes. A pass reads only the logs that grew or have
// something coming due, so an hourly pass costs little once the first,
// at startup, has read every log.
type Pruner struct {
	cfg Config

	mu   sync.Mutex
	seen map[string]seen
}

// seen is what a pass learnt of a log: the seq it read to, and when the
// next thing in it comes due, zero when nothing ever will.
type seen struct {
	seq uint64
	due time.Time
}

// New checks the wiring.
func New(cfg Config) (*Pruner, error) {
	if cfg.Journal == nil || cfg.Store == nil || cfg.Blobs == nil || cfg.Curation == nil || cfg.Clock == nil || cfg.Timers == nil {
		return nil, errors.New("retention: journal, store, blobs, curation, clock and timers are required")
	}
	if cfg.Every <= 0 {
		cfg.Every = DefaultEvery
	}
	if cfg.Live <= 0 {
		cfg.Live = DefaultLive
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Pruner{cfg: cfg, seen: map[string]seen{}}, nil
}

// Run passes once now, then every Every, until ctx ends.
func (p *Pruner) Run(ctx context.Context) {
	for {
		rep, err := p.Pass(ctx)
		if err != nil {
			p.cfg.Log.Warn("retention: a pass left logs unpruned; the next pass tries again", "err", err)
		}
		if !rep.empty() {
			p.cfg.Log.Info("retention: pruned",
				"audio_pruned", rep.AudioPruned, "audio_not_kept", rep.AudioNotKept,
				"logs_deleted", rep.LogsDeleted, "events_deleted", rep.EventsDeleted)
		}
		select {
		case <-ctx.Done():
			return
		case <-p.cfg.Timers.After(p.cfg.Every):
		}
	}
}

// Pass prunes every log once. A log that fails is left for the next pass
// and the rest go on; the error joins every failure.
func (p *Pruner) Pass(ctx context.Context) (Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var declined map[string]time.Time
	if p.cfg.NotKept != nil {
		declined = p.cfg.NotKept.Declined()
	}
	var rep Report
	if !p.cfg.Policy.prunes() && len(declined) == 0 {
		return rep, nil
	}
	ids, err := p.cfg.Store.Conversations(ctx)
	if err != nil {
		return rep, fmt.Errorf("retention: list logs: %w", err)
	}
	now := p.cfg.Clock.Now()
	var errs []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		seq, err := p.cfg.Store.LastSeq(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("retention: %s: %w", id, err))
			continue
		}
		if !p.read(id, seq, now, len(declined) > 0) {
			continue
		}
		due, err := p.prune(ctx, id, now, declined, &rep)
		if err != nil {
			errs = append(errs, fmt.Errorf("retention: %s: %w", id, err))
			delete(p.seen, id)
			continue
		}
		// Read to past what this pass appended, so its own records are
		// not growth; a deleted log reads as 0 and is forgotten.
		if seq, err = p.cfg.Store.LastSeq(ctx, id); err != nil || seq == 0 {
			delete(p.seen, id)
			continue
		}
		p.seen[id] = seen{seq: seq, due: due}
	}
	return rep, errors.Join(errs...)
}

// read decides whether a log is worth reading this pass: never seen, come
// due, or grown. A long-lived log grows by the minute, and what it grows by
// cannot come due before its due time already says, so only a declined
// clip it may now name makes its growth worth a read.
func (p *Pruner) read(id string, seq uint64, now time.Time, declined bool) bool {
	s, ok := p.seen[id]
	switch {
	case !ok, !s.due.IsZero() && !now.Before(s.due):
		return true
	case s.seq == seq:
		return false
	case longLived(id):
		return declined
	}
	return true
}

func longLived(id string) bool {
	return id == journal.HouseTimers || strings.HasPrefix(id, devicePrefix)
}

// prune handles one log and returns when it next needs a look; zero means
// nothing in it will ever come due.
func (p *Pruner) prune(ctx context.Context, id string, now time.Time, declined map[string]time.Time, rep *Report) (time.Time, error) {
	events, err := p.cfg.Store.Events(ctx, id)
	if err != nil || len(events) == 0 {
		return time.Time{}, err
	}
	switch {
	case id == journal.HouseTimers:
		return p.house(ctx, events, now, rep)
	case strings.HasPrefix(id, devicePrefix):
		return p.device(ctx, id, events, now, declined, rep)
	}
	return p.conversation(ctx, id, events, now, declined, rep)
}

// conversation deletes a session's log whole once its journal horizon has
// passed since its last activity, or prunes its audio clip by clip.
func (p *Pruner) conversation(ctx context.Context, id string, events []journal.Event, now time.Time, declined map[string]time.Time, rep *Report) (time.Time, error) {
	last, open := activity(events)
	if open && now.Sub(last) < p.cfg.Live {
		// ADR-0022: one live session per conversation, and this is its log.
		return now, nil
	}
	// Asked only once something is due: most logs a pass reads have nothing
	// to prune, and curation is three queries.
	var (
		asked, curated bool
		cerr           error
	)
	isCurated := func(string) bool {
		if !asked && !p.cfg.Policy.PruneCurated {
			asked = true
			curated, cerr = curation.Curated(ctx, p.cfg.Curation, id)
		}
		return curated || cerr != nil
	}
	horizon := p.journalHorizon(events)
	if horizon > 0 && now.Sub(last) >= horizon && !isCurated("") {
		return time.Time{}, p.deleteLog(ctx, id, events, rep)
	}
	due, err := p.audio(ctx, id, events, now, declined, isCurated, rep)
	if err == nil {
		err = cerr
	}
	if err != nil {
		return time.Time{}, err
	}
	switch {
	case curated:
		due = earliest(due, now.Add(recheckCurated))
	case horizon > 0:
		due = earliest(due, last.Add(horizon))
	}
	return due, nil
}

// activity is when a conversation last did something, and whether its log
// leaves a session open. Dropped audio is recorded a horizon later and is
// no activity, or every pruned clip would put the log's own deletion off.
func activity(events []journal.Event) (last time.Time, open bool) {
	for _, e := range events {
		switch e.Kind {
		case journal.KindAudioDropped:
			continue
		case journal.KindSessionOpened:
			open = true
		case journal.KindSessionClosed:
			open = false
		}
		last = e.At
	}
	return last, open
}

// journalHorizon is how long a conversation is kept: the longest of the
// satellites it was on, and forever when any of them keeps forever.
func (p *Pruner) journalHorizon(events []journal.Event) time.Duration {
	var longest time.Duration
	named := false
	for _, e := range events {
		if e.Kind != journal.KindSessionOpened {
			continue
		}
		named = true
		h := p.cfg.Policy.For(e.Fields["satellite"]).Journal
		if h == 0 {
			return 0
		}
		longest = max(longest, h)
	}
	if !named {
		return p.cfg.Policy.House.Journal
	}
	return longest
}

// deleteLog removes a conversation's audio, its curation rows and then its
// log, in that order: the log is what a failed pass finds it again by.
func (p *Pruner) deleteLog(ctx context.Context, id string, events []journal.Event, rep *Report) error {
	gone := dropped(events)
	for _, c := range clips(events) {
		if gone[c.ref] {
			continue
		}
		if err := p.cfg.Blobs.Remove(ctx, c.ref); err != nil {
			return err
		}
		if p.cfg.NotKept != nil {
			p.cfg.NotKept.Settle(c.ref)
		}
	}
	if err := p.cfg.Curation.Forget(ctx, id); err != nil {
		return err
	}
	if err := p.cfg.Store.DeleteLog(ctx, id); err != nil {
		return err
	}
	rep.LogsDeleted++
	return nil
}

// device trims a satellite's own log past its journal horizon, keeping the
// wakes a reviewer confirmed and the log's last event, then prunes the
// audio of what is left.
func (p *Pruner) device(ctx context.Context, id string, events []journal.Event, now time.Time, declined map[string]time.Time, rep *Report) (time.Time, error) {
	sat := strings.TrimPrefix(id, devicePrefix)
	h := p.cfg.Policy.For(sat)
	confirmed := map[uint64]bool{}
	if !p.cfg.Policy.PruneCurated {
		var err error
		if confirmed, err = curation.ConfirmedWakes(ctx, p.cfg.Curation, id); err != nil {
			return time.Time{}, err
		}
	}
	kept, due := events, time.Time{}
	if h.Journal > 0 {
		var err error
		if kept, due, err = p.trimDevice(ctx, id, events, now, h.Journal, confirmed, rep); err != nil {
			return time.Time{}, err
		}
	}
	confirmedRefs := map[string]bool{}
	for _, e := range kept {
		if confirmed[e.Seq] {
			for _, ref := range refsOf(e) {
				confirmedRefs[ref] = true
			}
		}
	}
	audioDue, err := p.audio(ctx, id, kept, now, declined, func(ref string) bool { return confirmedRefs[ref] }, rep)
	if err != nil {
		return time.Time{}, err
	}
	// What the log grows by from now cannot come due sooner than this.
	for _, d := range []time.Duration{h.Audio, h.Journal} {
		if d > 0 {
			due = earliest(due, now.Add(d))
		}
	}
	return earliest(due, audioDue), nil
}

// trimDevice deletes the device log's events older than the horizon and
// returns what is left, and when the next of it comes due. An
// audio_dropped stays while what it names does.
func (p *Pruner) trimDevice(ctx context.Context, id string, events []journal.Event, now time.Time, horizon time.Duration, confirmed map[uint64]bool, rep *Report) ([]journal.Event, time.Time, error) {
	lastSeq := events[len(events)-1].Seq
	forever := func(e journal.Event) bool { return e.Seq == lastSeq || confirmed[e.Seq] }
	old := func(e journal.Event) bool { return now.Sub(e.At) >= horizon && !forever(e) }
	named := map[string]bool{}
	for _, e := range events {
		if !old(e) && e.Kind != journal.KindAudioDropped {
			for _, ref := range refsOf(e) {
				named[ref] = true
			}
		}
	}
	gone := dropped(events)
	var (
		doomed, wakes []uint64
		kept          []journal.Event
		due           time.Time
	)
	for _, e := range events {
		if !old(e) || (e.Kind == journal.KindAudioDropped && named[e.Fields["audio_ref"]]) {
			kept = append(kept, e)
			if !old(e) && !forever(e) {
				due = earliest(due, e.At.Add(horizon))
			}
			continue
		}
		for _, ref := range refsOf(e) {
			if gone[ref] {
				continue
			}
			if err := p.cfg.Blobs.Remove(ctx, ref); err != nil {
				return nil, time.Time{}, err
			}
		}
		doomed = append(doomed, e.Seq)
		if e.Kind == journal.KindWakeRejected {
			wakes = append(wakes, e.Seq)
		}
	}
	if len(doomed) == 0 {
		return events, due, nil
	}
	if err := p.cfg.Curation.ForgetWakes(ctx, id, wakes); err != nil {
		return nil, time.Time{}, err
	}
	if err := p.cfg.Store.DeleteEvents(ctx, id, doomed); err != nil {
		return nil, time.Time{}, err
	}
	rep.EventsDeleted += len(doomed)
	return kept, due, nil
}

// house trims the house log of timers that ended before the house's
// journal horizon, a timer's events together so every remaining end still
// has its start, and the last event's timer kept so the seq never rewinds.
func (p *Pruner) house(ctx context.Context, events []journal.Event, now time.Time, rep *Report) (time.Time, error) {
	horizon := p.cfg.Policy.House.Journal
	if horizon == 0 {
		return time.Time{}, nil
	}
	type timer struct {
		seqs  []uint64
		ended time.Time
	}
	timers := map[string]*timer{}
	var order []string
	for _, e := range events {
		id := e.Fields["timer_id"]
		t := timers[id]
		if t == nil {
			t = &timer{}
			timers[id] = t
			order = append(order, id)
		}
		t.seqs = append(t.seqs, e.Seq)
		if e.Kind == journal.KindTimerCancelled || e.Kind == journal.KindTimerFinished {
			t.ended = e.At
		}
	}
	lastTimer := events[len(events)-1].Fields["timer_id"]
	var doomed []uint64
	due := now.Add(horizon)
	for _, id := range order {
		t := timers[id]
		switch {
		case id == lastTimer, t.ended.IsZero():
		case now.Sub(t.ended) >= horizon:
			doomed = append(doomed, t.seqs...)
		default:
			due = earliest(due, t.ended.Add(horizon))
		}
	}
	if len(doomed) > 0 {
		if err := p.cfg.Store.DeleteEvents(ctx, journal.HouseTimers, doomed); err != nil {
			return time.Time{}, err
		}
		rep.EventsDeleted += len(doomed)
	}
	return due, nil
}

// clip is one blob a log names, the satellite it was recorded on, and the
// last time an event named it: a barge-in names its utterance's blob before
// the utterance does.
type clip struct {
	ref       string
	satellite string
	at        time.Time
}

// clips are a log's blobs in the order first named. The satellite is the
// one whose session was open when the event was recorded: the audio
// stream belongs to the device (SPEC §4.5).
func clips(events []journal.Event) []clip {
	var (
		out []clip
		at  = map[string]int{}
		sat string
	)
	for _, e := range events {
		if e.Kind == journal.KindSessionOpened {
			sat = e.Fields["satellite"]
		}
		for _, ref := range refsOf(e) {
			i, ok := at[ref]
			if !ok {
				at[ref] = len(out)
				out = append(out, clip{ref: ref, satellite: sat})
				i = len(out) - 1
			}
			out[i].at = e.At
		}
	}
	return out
}

// refsOf is every blob an event names: its own and its second channel's.
func refsOf(e journal.Event) []string {
	var refs []string
	if e.AudioRef != "" {
		refs = append(refs, e.AudioRef)
	}
	if r := e.Fields["second_audio_ref"]; r != "" {
		refs = append(refs, r)
	}
	return refs
}

// dropped is every ref the log already says is not kept.
func dropped(events []journal.Event) map[string]bool {
	gone := map[string]bool{}
	for _, e := range events {
		if e.Kind == journal.KindAudioDropped {
			gone[e.Fields["audio_ref"]] = true
		}
	}
	return gone
}

// audio journals each clip the disk guard declined, and removes each clip
// past its satellite's audio horizon that keep does not, recording it in
// the log that names it. It returns when the next clip comes due.
func (p *Pruner) audio(ctx context.Context, id string, events []journal.Event, now time.Time, declined map[string]time.Time, keep func(ref string) bool, rep *Report) (time.Time, error) {
	satellite := ""
	if s, ok := strings.CutPrefix(id, devicePrefix); ok {
		satellite = s
	}
	gone := dropped(events)
	var due time.Time
	for _, c := range clips(events) {
		if gone[c.ref] {
			continue
		}
		if satellite != "" {
			c.satellite = satellite
		}
		if _, ok := declined[c.ref]; ok {
			if err := p.drop(ctx, id, c.ref, "disk_low", 0); err != nil {
				return time.Time{}, err
			}
			p.cfg.NotKept.Settle(c.ref)
			rep.AudioNotKept++
			continue
		}
		h := p.cfg.Policy.For(c.satellite).Audio
		if h == 0 {
			continue
		}
		if now.Sub(c.at) < h {
			due = earliest(due, c.at.Add(h))
			continue
		}
		if keep(c.ref) {
			continue
		}
		if err := p.cfg.Blobs.Remove(ctx, c.ref); err != nil {
			return time.Time{}, err
		}
		if err := p.drop(ctx, id, c.ref, "retention", h); err != nil {
			return time.Time{}, err
		}
		rep.AudioPruned++
	}
	return due, nil
}

// drop records that a clip is not kept, in the log that names it.
func (p *Pruner) drop(ctx context.Context, id, ref, reason string, horizon time.Duration) error {
	fields := map[string]string{"audio_ref": ref, "reason": reason}
	if horizon > 0 {
		fields["days"] = strconv.Itoa(int(horizon / (24 * time.Hour)))
	}
	_, err := p.cfg.Journal.Append(ctx, id, journal.Record{Kind: journal.KindAudioDropped, Fields: fields})
	return err
}

// earliest is the sooner of two times, a zero one meaning none.
func earliest(a, b time.Time) time.Time {
	switch {
	case a.IsZero():
		return b
	case b.IsZero() || a.Before(b):
		return a
	}
	return b
}
