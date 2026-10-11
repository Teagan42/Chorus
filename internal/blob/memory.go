package blob

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"slices"
	"sync"
)

// Memory keeps blobs in memory. It is the hermetic tier's store (`task test`
// touches no disk service) and the double any test that only cares about the
// reference should use.
type Memory struct {
	mu    sync.Mutex
	blobs map[string][]byte
}

func NewMemory() *Memory { return &Memory{blobs: map[string][]byte{}} }

func (m *Memory) Create(_ context.Context, key string) (Writer, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	return &memWriter{store: m, key: key}, nil
}

func (m *Memory) Open(_ context.Context, ref string) (io.ReadCloser, error) {
	key, err := KeyFor(ref)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	b, ok := m.blobs[key]
	m.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("blob: open %q: %w", ref, fs.ErrNotExist)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// Remove deletes a committed blob; one already gone is no error.
func (m *Memory) Remove(_ context.Context, ref string) error {
	key, err := KeyFor(ref)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.blobs, key)
	return nil
}

// Refs lists what has been committed, in order, for assertions.
func (m *Memory) Refs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := make([]string, 0, len(m.blobs))
	for _, key := range slices.Sorted(maps.Keys(m.blobs)) {
		refs = append(refs, RefFor(key))
	}
	return refs
}

// Bytes is a committed blob's contents, for assertions.
func (m *Memory) Bytes(ref string) ([]byte, bool) {
	key, err := KeyFor(ref)
	if err != nil {
		return nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[key]
	return b, ok
}

type memWriter struct {
	store *Memory
	key   string
	buf   bytes.Buffer
	done  bool
}

func (w *memWriter) Write(p []byte) (int, error) {
	if w.done {
		return 0, fmt.Errorf("blob: %q is already closed", w.key)
	}
	return w.buf.Write(p)
}

func (w *memWriter) Commit() (string, error) {
	if w.done {
		return RefFor(w.key), nil
	}
	w.done = true
	w.store.mu.Lock()
	w.store.blobs[w.key] = slices.Clone(w.buf.Bytes())
	w.store.mu.Unlock()
	return RefFor(w.key), nil
}

func (w *memWriter) Abort() error {
	w.done = true
	return nil
}
