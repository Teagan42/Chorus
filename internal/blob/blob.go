// Package blob stores the audio a journal event refers to. Events carry a
// reference and never the samples (ADR-0007, SPEC §8).
//
// Blobs hold raw PCM in the device's format -- 16 kHz, signed 16-bit, mono,
// little-endian, per channel -- not WAV. The format is an invariant of the
// whole system rather than a property of a file, and a self-describing
// container would need the length patched in after the fact, which costs a
// seek on every utterance to save a reader 44 bytes.
package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Scheme prefixes every reference. Stored in the journal, so it is a format
// the database already holds: do not change it without a migration.
const Scheme = "blob://"

// maxKeyLen bounds a key so a reference cannot exceed a filesystem's path
// limit and fail only once an utterance is already over.
const maxKeyLen = 512

// Store persists audio blobs. Implementations are safe for concurrent use.
type Store interface {
	// Create opens a blob for writing at key. Nothing is readable until the
	// returned writer is committed.
	Create(ctx context.Context, key string) (Writer, error)

	// Open reads a committed blob. A missing blob wraps fs.ErrNotExist.
	Open(ctx context.Context, ref string) (io.ReadCloser, error)
}

// Writer accumulates one blob. Exactly one of Commit or Abort must be called;
// both are idempotent.
type Writer interface {
	io.Writer

	// Commit publishes the blob and returns the reference the journal stores.
	Commit() (string, error)

	// Abort discards it. A partial blob must never become readable: a corpus
	// cannot distinguish a truncated recording from a complete one later.
	Abort() error
}

// RefFor is the reference a key is stored under.
func RefFor(key string) string { return Scheme + key }

// KeyFor validates a reference and returns its key.
func KeyFor(ref string) (string, error) {
	if !strings.HasPrefix(ref, Scheme) {
		return "", fmt.Errorf("blob: %q is not a %s reference", ref, Scheme)
	}
	key := strings.TrimPrefix(ref, Scheme)
	if err := checkKey(key); err != nil {
		return "", err
	}
	return key, nil
}

// checkKey rejects anything that would not resolve inside the store. Keys are
// built from generated conversation and call ids, but a store that resolves
// "../" is a write primitive aimed at the host, so this is enforced rather
// than assumed.
func checkKey(key string) error {
	switch {
	case key == "":
		return errors.New("blob: empty key")
	case len(key) > maxKeyLen:
		return fmt.Errorf("blob: key is %d bytes, over the %d limit", len(key), maxKeyLen)
	case strings.HasPrefix(key, "/"), strings.Contains(key, `\`):
		return fmt.Errorf("blob: key %q must be relative", key)
	case key != path.Clean(key):
		// Catches "..", "." and doubled separators in one test: a clean path is
		// its own canonical form.
		return fmt.Errorf("blob: key %q is not canonical", key)
	case strings.HasPrefix(key, "../"):
		return fmt.Errorf("blob: key %q escapes the store", key)
	}
	return nil
}

// Dir stores blobs under a filesystem root. This is the deployment default:
// one household generates ~115 MB/day (SPEC §8), which wants a disk and a
// retention policy rather than an object store.
type Dir struct{ root string }

// NewDir prepares a root, creating it if needed.
func NewDir(root string) (*Dir, error) {
	if root == "" {
		return nil, errors.New("blob: no root directory")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("blob: prepare root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("blob: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("blob: root %q is not a directory", root)
	}
	return &Dir{root: root}, nil
}

func (d *Dir) Create(_ context.Context, key string) (Writer, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	final := filepath.Join(d.root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return nil, fmt.Errorf("blob: prepare %q: %w", key, err)
	}
	// Written to a sibling temp file and renamed on commit, so a reader
	// following a journal reference never sees a growing file and a crash
	// leaves no half blob behind.
	f, err := os.CreateTemp(filepath.Dir(final), ".partial-*")
	if err != nil {
		return nil, fmt.Errorf("blob: open %q: %w", key, err)
	}
	return &dirWriter{key: key, final: final, f: f}, nil
}

func (d *Dir) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	key, err := KeyFor(ref)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(d.root, filepath.FromSlash(key)))
	if err != nil {
		return nil, fmt.Errorf("blob: open %q: %w", ref, err)
	}
	return f, nil
}

type dirWriter struct {
	key   string
	final string
	f     *os.File
	done  bool
}

func (w *dirWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, fmt.Errorf("blob: %q is already closed", w.key)
	}
	return w.f.Write(p)
}

func (w *dirWriter) Commit() (string, error) {
	if w.done {
		return RefFor(w.key), nil
	}
	w.done = true
	// Synced before the rename: the rename is what makes the blob visible, so
	// a reference in the journal must not be able to outlive its contents.
	if err := w.f.Sync(); err != nil {
		_ = w.f.Close()
		_ = os.Remove(w.f.Name())
		return "", fmt.Errorf("blob: sync %q: %w", w.key, err)
	}
	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.f.Name())
		return "", fmt.Errorf("blob: close %q: %w", w.key, err)
	}
	if err := os.Rename(w.f.Name(), w.final); err != nil {
		_ = os.Remove(w.f.Name())
		return "", fmt.Errorf("blob: publish %q: %w", w.key, err)
	}
	return RefFor(w.key), nil
}

func (w *dirWriter) Abort() error {
	if w.done {
		return nil
	}
	w.done = true
	_ = w.f.Close()
	if err := os.Remove(w.f.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("blob: discard %q: %w", w.key, err)
	}
	return nil
}
