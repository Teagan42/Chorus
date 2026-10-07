package blob_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teaganglenn/chorus/internal/blob"
)

// stores exercises every implementation against the same contract, so a test
// tier that swaps the backend cannot swap the semantics with it.
func stores(t *testing.T) map[string]blob.Store {
	t.Helper()
	return map[string]blob.Store{
		"dir":    mustDir(t, t.TempDir()),
		"memory": blob.NewMemory(),
	}
}

func mustDir(t *testing.T, root string) *blob.Dir {
	t.Helper()
	d, err := blob.NewDir(root)
	if err != nil {
		t.Fatalf("new dir: %v", err)
	}
	return d
}

// verifies SPEC §8
func TestAStoredBlobReadsBackByItsRef(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			want := []byte{0x01, 0x02, 0xfe, 0xff}

			w, err := store.Create(ctx, "conv-1/tts/call-1")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := w.Write(want); err != nil {
				t.Fatalf("write: %v", err)
			}
			ref, err := w.Commit()
			if err != nil {
				t.Fatalf("commit: %v", err)
			}
			if !strings.HasPrefix(ref, blob.Scheme) {
				t.Errorf("ref = %q, want a %s reference", ref, blob.Scheme)
			}

			r, err := store.Open(ctx, ref)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer r.Close()
			got, err := io.ReadAll(r)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("read back %x, want %x", got, want)
			}
		})
	}
}

// verifies SPEC §8
func TestAnUncommittedBlobIsNotReadable(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			w, err := store.Create(ctx, "conv-1/tts/abandoned")
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if _, err := w.Write([]byte("partial")); err != nil {
				t.Fatalf("write: %v", err)
			}
			// A crash partway through an utterance must not leave a truncated
			// blob that reads as a complete one: a corpus cannot tell them apart
			// after the fact.
			if err := w.Abort(); err != nil {
				t.Fatalf("abort: %v", err)
			}
			if _, err := store.Open(ctx, blob.Scheme+"conv-1/tts/abandoned"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("open after abort: err = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

// verifies SPEC §8
func TestCommitIsAtomicallyVisible(t *testing.T) {
	root := t.TempDir()
	store := mustDir(t, root)
	ctx := context.Background()

	w, err := store.Create(ctx, "conv-1/tts/call-1")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := w.Write([]byte("half")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Nothing at the final path yet, so a reader following a journal ref never
	// sees a growing file.
	final := filepath.Join(root, "conv-1", "tts", "call-1")
	if _, err := os.Stat(final); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat before commit: err = %v, want fs.ErrNotExist", err)
	}
	if _, err := w.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := os.Stat(final); err != nil {
		t.Errorf("stat after commit: %v", err)
	}
}

// verifies SPEC §8
func TestKeysThatEscapeTheRootAreRejected(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			// Keys are built from conversation and call ids. Those are generated,
			// but a blob store that resolves "../" is a file-write primitive, so
			// this is enforced rather than assumed.
			for _, key := range []string{
				"", "/absolute", "../escape", "conv/../../escape",
				"conv//empty", "conv/./dot", strings.Repeat("a", 1024),
			} {
				if _, err := store.Create(ctx, key); err == nil {
					t.Errorf("Create(%q) succeeded, want an error", key)
				}
			}
		})
	}
}

// verifies SPEC §8
func TestAnUnknownRefIsNotFound(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			if _, err := store.Open(ctx, blob.Scheme+"conv-1/tts/missing"); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("open missing: err = %v, want fs.ErrNotExist", err)
			}
			// A ref from another scheme is a wiring mistake, not a miss.
			for _, ref := range []string{"", "conv-1/tts/x", "s3://bucket/x"} {
				if _, err := store.Open(ctx, ref); err == nil {
					t.Errorf("Open(%q) succeeded, want an error", ref)
				}
			}
		})
	}
}

// verifies SPEC §8
func TestRefsRoundTripThroughKeys(t *testing.T) {
	const key = "conv-1/mic/utterance-3"
	ref := blob.RefFor(key)
	got, err := blob.KeyFor(ref)
	if err != nil {
		t.Fatalf("key for %q: %v", ref, err)
	}
	if got != key {
		t.Errorf("round trip = %q, want %q", got, key)
	}
}

func TestNewDirRejectsAnUnusableRoot(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := blob.NewDir(file); err == nil {
		t.Error("NewDir on a regular file succeeded, want an error")
	}
	if _, err := blob.NewDir(""); err == nil {
		t.Error("NewDir with no root succeeded, want an error")
	}
}
