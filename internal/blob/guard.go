package blob

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// DefaultFloor is the free space below which audio is not written: room
// for the database and the logs to keep working when the disk fills.
const DefaultFloor = 1 << 30

// declinedFor is how long a declined ref waits for retention to journal it.
// One no event ever names, as an utterance its session never heard, is
// let go after it.
const declinedFor = 24 * time.Hour

// Disk reads the free space on the filesystem holding the blobs. Tests
// inject it; only DirFree reads a real disk (CONTRIBUTING §1).
type Disk interface {
	Free() (uint64, error)
}

// Clock is the guard's own seam, so this package needs no journal.
type Clock interface {
	Now() time.Time
}

// GuardConfig wires a Guarded store.
type GuardConfig struct {
	Disk Disk
	// Floor is the free space, in bytes, below which audio is not written.
	Floor uint64
	Clock Clock
	// Log hears each crossing of the floor once. Nil discards.
	Log *slog.Logger
}

// Guarded writes nothing while the disk is below its floor, and holds each
// ref it declined so retention can journal that the audio was not kept
// (ADR-0065). The writer still commits to the ref: the turn that names it
// goes on as it would, so the household hears the same.
type Guarded struct {
	Store
	cfg GuardConfig

	mu       sync.Mutex
	low      bool
	unread   bool
	declined map[string]time.Time
}

// Guard wraps s.
func Guard(s Store, cfg GuardConfig) *Guarded {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Guarded{Store: s, cfg: cfg, declined: map[string]time.Time{}}
}

// Create writes through, or declines while the disk is below its floor.
func (g *Guarded) Create(ctx context.Context, key string) (Writer, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	if g.isLow() {
		return &declinedWriter{guard: g, key: key}, nil
	}
	return g.Store.Create(ctx, key)
}

// Remove removes through the wrapped store, when it can.
func (g *Guarded) Remove(ctx context.Context, ref string) error {
	if r, ok := g.Store.(interface {
		Remove(context.Context, string) error
	}); ok {
		return r.Remove(ctx, ref)
	}
	return nil
}

// isLow reads the disk and logs a crossing of the floor, once each way.
// A disk it cannot read is not known to be full, so it writes.
func (g *Guarded) isLow() bool {
	free, err := g.cfg.Disk.Free()
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		if !g.unread {
			g.cfg.Log.Warn("blob disk unreadable: audio is kept, unguarded, until it can be read", "err", err)
		}
		g.unread = true
		return false
	}
	g.unread = false
	low := free < g.cfg.Floor
	switch {
	case low && !g.low:
		g.cfg.Log.Warn("blob disk below its floor: audio is not kept until it has room; turns go on", "free", free, "floor", g.cfg.Floor)
	case !low && g.low:
		g.cfg.Log.Info("blob disk has room again: audio is kept", "free", free, "floor", g.cfg.Floor)
	}
	g.low = low
	return low
}

// Declined is each ref not written and not yet settled, with when.
func (g *Guarded) Declined() map[string]time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	cutoff := g.cfg.Clock.Now().Add(-declinedFor)
	maps.DeleteFunc(g.declined, func(_ string, at time.Time) bool { return at.Before(cutoff) })
	return maps.Clone(g.declined)
}

// Settle lets go of a declined ref once the journal says it was not kept.
func (g *Guarded) Settle(ref string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.declined, ref)
}

// declinedWriter takes the audio and keeps none of it.
type declinedWriter struct {
	guard *Guarded
	key   string
	done  bool
}

func (w *declinedWriter) Write(p []byte) (int, error) { return len(p), nil }

func (w *declinedWriter) Commit() (string, error) {
	ref := RefFor(w.key)
	if !w.done {
		w.done = true
		w.guard.mu.Lock()
		w.guard.declined[ref] = w.guard.cfg.Clock.Now()
		w.guard.mu.Unlock()
	}
	return ref, nil
}

func (w *declinedWriter) Abort() error {
	w.done = true
	return nil
}
