package main

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// The disk guard's floor defaults to a GiB, 0 turns it off, and anything
// else that is not a number of MiB fails the start naming the variable.
//
// verifies SPEC §8, §13
func TestTheBlobFloorIsAGiBUnlessSaidOtherwise(t *testing.T) {
	floor, err := configFromEnv(lookup(complete())).blobFloor()
	if err != nil || floor != blob.DefaultFloor {
		t.Errorf("default floor = %d, %v; want %d", floor, err, blob.DefaultFloor)
	}
	env := complete()
	env[blobMinFreeEnv] = "0"
	if floor, err := configFromEnv(lookup(env)).blobFloor(); err != nil || floor != 0 {
		t.Errorf("0 = %d, %v; want the guard off", floor, err)
	}
	env[blobMinFreeEnv] = "lots"
	err = configFromEnv(lookup(env)).validate()
	if err == nil || !strings.Contains(err.Error(), blobMinFreeEnv) {
		t.Errorf("err = %v; want it to name %s", err, blobMinFreeEnv)
	}
}

// The kitchen keeps 30 days of audio. A conversation from 40 days before
// the daemon starts loses its clip in the pass chorusd runs at startup,
// before anyone speaks, and the log says so.
//
// verifies SPEC §8
func TestTheDaemonPrunesAtStartup(t *testing.T) {
	ctx := context.Background()
	store, blobs := journal.NewMemStore(), blob.NewMemory()
	old := journal.New(store, journal.FixedClock(epoch.AddDate(0, 0, -40)), journal.Versions{})
	w, err := blobs.Create(ctx, "mic/kitchen-old")
	if err != nil {
		t.Fatal(err)
	}
	ref, err := w.Commit()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alan"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: ref, Fields: map[string]string{"text": "what's the weather"}},
		{Kind: journal.KindSessionClosed, Fields: map[string]string{"reason": "model_ended", "satellite": "kitchen"}},
	} {
		if _, err := old.Append(ctx, "conv-kitchen-old", r); err != nil {
			t.Fatal(err)
		}
	}
	inv := inventory()
	inv.Satellites[0].Retention = &config.Retention{Audio: config.Days{N: 30, Set: true}}

	newRig(t, inv, func(d *deps) {
		d.Store, d.Blobs = store, blobs
		d.Prune = &pruneDeps{Curation: curation.NewMemStore(), Blobs: blobs}
	})
	await(t, "the startup pass", func() bool {
		_, err := blobs.Open(ctx, ref)
		return errors.Is(err, fs.ErrNotExist)
	})
	await(t, "the drop to be journalled", func() bool {
		events, _ := store.Events(ctx, "conv-kitchen-old")
		return len(events) == 4 && events[3].Kind == journal.KindAudioDropped
	})
}
