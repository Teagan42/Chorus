package evalsuite

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
)

// Entry is one cached take: what the engine did, and how long it took to
// first speak, so scoring again needs no model.
type Entry struct {
	Versions      journal.Versions `json:"versions"`
	Take          rerun.Take       `json:"take"`
	FirstSpeechNS int64            `json:"first_speech_ns"`
	Measured      bool             `json:"measured"`
}

// Cache keeps takes by key. A failed run is never put.
type Cache interface {
	Get(key string) (Entry, bool, error)
	Put(key string, e Entry) error
}

// MemCache is a Cache for one process.
type MemCache struct {
	mu sync.Mutex
	m  map[string]Entry
}

// Get is the entry under key.
func (c *MemCache) Get(key string) (Entry, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return e, ok, nil
}

// Put keeps e under key.
func (c *MemCache) Put(key string, e Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Entry{}
	}
	c.m[key] = e
	return nil
}

// DirCache keeps each entry as a JSON file under Dir, named by its key.
type DirCache struct{ Dir string }

func (c DirCache) path(key string) string { return filepath.Join(c.Dir, key[:2], key+".json") }

// Get reads the entry under key; a key never put is no error.
func (c DirCache) Get(key string) (Entry, bool, error) {
	b, err := os.ReadFile(c.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return Entry{}, false, nil
	}
	if err != nil {
		return Entry{}, false, fmt.Errorf("eval cache: %w", err)
	}
	var e Entry
	// A torn or hand-edited file is asked again, not trusted.
	ok := json.Unmarshal(b, &e) == nil
	return e, ok, nil
}

// Put writes e under key, whole or not at all.
func (c DirCache) Put(key string, e Entry) error {
	p := c.path(key)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".put-*")
	if err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("eval cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("eval cache: %w", err)
	}
	return nil
}
