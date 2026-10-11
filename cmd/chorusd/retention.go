package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/retention"
)

// blobMinFreeEnv is the disk guard's floor, in MiB: below it audio is not
// written (ADR-0065). 0 turns the guard off.
const blobMinFreeEnv = "CHORUS_BLOB_MIN_FREE_MIB"

// blobFloor is the guard's floor in bytes, blob.DefaultFloor when unset.
func (c Config) blobFloor() (uint64, error) {
	if c.BlobMinFreeMiB == "" {
		return blob.DefaultFloor, nil
	}
	mib, err := strconv.ParseUint(c.BlobMinFreeMiB, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a whole number of MiB (0 turns the guard off)", blobMinFreeEnv, c.BlobMinFreeMiB)
	}
	return mib << 20, nil
}

// pruneDeps is what retention needs beyond the daemon's own journal, clock
// and inventory.
type pruneDeps struct {
	Curation retention.Curation
	Blobs    retention.Blobs
	// NotKept is the disk guard, nil when it is off.
	NotKept retention.NotKept
}

// prune runs retention until ctx ends: a pass now, then hourly. It writes
// through the daemon's journal, whose per-log lock orders its records with
// the sessions' (SPEC §8, ADR-0065).
func (d *daemon) prune(ctx context.Context) {
	store, ok := d.Store.(retention.Store)
	if !ok {
		d.Log.Warn("retention is off: the journal store cannot list or delete logs")
		return
	}
	policy := retention.FromConfig(d.inv)
	p, err := retention.New(retention.Config{
		Journal: d.journal, Store: store, Blobs: d.Prune.Blobs, Curation: d.Prune.Curation,
		NotKept: d.Prune.NotKept, Clock: d.Clock, Timers: d.Timers, Policy: policy, Log: d.Log,
	})
	if err != nil {
		d.Log.Warn("retention is off", "err", err)
		return
	}
	d.Log.Info("retention", "house_audio", policy.House.Audio, "house_journal", policy.House.Journal,
		"prune_curated", policy.PruneCurated, "guarded", d.Prune.NotKept != nil)
	p.Run(ctx)
}
