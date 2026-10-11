package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/blob"
)

// A removed blob is gone for every reader, and removing one already gone
// is no error: retention may be asked twice, after a crash between the
// delete and the journal's record of it.
//
// verifies SPEC §8
func TestARemovedBlobIsGoneAndRemovingItAgainIsNoError(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			ref := put(t, store, "mic/kitchen-0901", []byte("what's the weather"))
			r, ok := store.(interface {
				Remove(context.Context, string) error
			})
			if !ok {
				t.Fatalf("%T cannot remove", store)
			}
			if err := r.Remove(ctx, ref); err != nil {
				t.Fatalf("remove: %v", err)
			}
			if _, err := store.Open(ctx, ref); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("open after remove = %v, want not exist", err)
			}
			if err := r.Remove(ctx, ref); err != nil {
				t.Errorf("second remove = %v, want nil", err)
			}
			if err := r.Remove(ctx, "../etc/passwd"); err == nil {
				t.Error("removing a ref outside the store succeeded")
			}
		})
	}
}

// disk is the blob directory's free space as a test sets it.
type disk struct {
	free uint64
	err  error
}

func (d *disk) Free() (uint64, error) { return d.free, d.err }

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// Below the floor the kitchen's utterance is not written, but the session
// still gets its reference, so what the household hears does not change;
// the ref is held as declined for retention to journal. With room again
// the next one is written. Each crossing is logged once, not per blob.
//
// verifies SPEC §8
func TestALowDiskDeclinesTheAudioButNotTheTurn(t *testing.T) {
	mem := blob.NewMemory()
	d := &disk{free: 512 << 20}
	clk := &clock{now: time.Date(2026, 10, 9, 7, 42, 0, 0, time.UTC)}
	var logged bytes.Buffer
	g := blob.Guard(mem, blob.GuardConfig{
		Disk: d, Floor: blob.DefaultFloor, Clock: clk,
		Log: slog.New(slog.NewTextHandler(&logged, nil)),
	})

	low1 := put(t, g, "mic/kitchen-1", []byte("turn on the kettle"))
	low2 := put(t, g, "mic/kitchen-1.ch1", []byte("turn on the kettle"))
	if low1 != "blob://mic/kitchen-1" {
		t.Errorf("ref = %q; the turn still needs its reference", low1)
	}
	if len(mem.Refs()) != 0 {
		t.Errorf("wrote %v below the floor", mem.Refs())
	}
	if got := keys(g.Declined()); !slices.Equal(got, []string{low1, low2}) {
		t.Errorf("declined = %v, want both channels", got)
	}

	d.free = 4 << 30
	kept := put(t, g, "mic/kitchen-2", []byte("thanks"))
	if !slices.Equal(mem.Refs(), []string{kept}) {
		t.Errorf("refs = %v, want the second utterance once there is room", mem.Refs())
	}

	out := logged.String()
	if n := strings.Count(out, "below its floor"); n != 1 {
		t.Errorf("logged the low disk %d times, want once:\n%s", n, out)
	}
	if n := strings.Count(out, "room again"); n != 1 {
		t.Errorf("logged the recovery %d times, want once:\n%s", n, out)
	}
}

// Retention settles a declined ref once it has journalled it, and one that
// no event ever named, such as an utterance its session never heard, is let
// go after a day rather than held for the life of the process.
//
// verifies SPEC §8
func TestADeclinedRefIsHeldUntilSettledOrADayOld(t *testing.T) {
	d := &disk{free: 0}
	clk := &clock{now: time.Date(2026, 10, 9, 7, 42, 0, 0, time.UTC)}
	g := blob.Guard(blob.NewMemory(), blob.GuardConfig{Disk: d, Floor: blob.DefaultFloor, Clock: clk})
	journalled := put(t, g, "mic/kitchen-1", []byte("turn on the kettle"))
	orphan := put(t, g, "mic/kitchen-2", []byte("never heard"))

	g.Settle(journalled)
	if got := keys(g.Declined()); !slices.Equal(got, []string{orphan}) {
		t.Errorf("declined = %v, want only the orphan", got)
	}
	clk.now = clk.now.Add(25 * time.Hour)
	if got := g.Declined(); len(got) != 0 {
		t.Errorf("declined = %v a day on, want none", got)
	}
}

// A disk the guard cannot read is not known to be full, so the audio is
// written: losing a recording to a broken probe is worse than a full disk.
//
// verifies SPEC §8
func TestAnUnreadableDiskStillKeepsTheAudio(t *testing.T) {
	mem := blob.NewMemory()
	g := blob.Guard(mem, blob.GuardConfig{
		Disk: &disk{err: errors.New("statfs: no such device")}, Floor: blob.DefaultFloor,
		Clock: &clock{},
	})
	ref := put(t, g, "mic/kitchen-1", []byte("turn on the kettle"))
	if !slices.Equal(mem.Refs(), []string{ref}) {
		t.Errorf("refs = %v, want the utterance written", mem.Refs())
	}
}

func put(t *testing.T, s blob.Store, key string, pcm []byte) string {
	t.Helper()
	w, err := s.Create(context.Background(), key)
	if err != nil {
		t.Fatalf("create %s: %v", key, err)
	}
	if _, err := w.Write(pcm); err != nil {
		t.Fatalf("write %s: %v", key, err)
	}
	ref, err := w.Commit()
	if err != nil {
		t.Fatalf("commit %s: %v", key, err)
	}
	return ref
}

func keys(m map[string]time.Time) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
