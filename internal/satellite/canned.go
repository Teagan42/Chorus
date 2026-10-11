package satellite

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sync"
)

// Canned is a Synth that says a few fixed lines from audio rendered ahead of
// time, and hands everything else to the synthesiser it wraps.
//
// The session apologises when the voice fails mid-turn (SPEC §7), which is
// the one line only ever needed while the synthesiser is down: it has to be
// rendered before it is needed, or it is never heard (ADR-0051). Kept as the
// segments a stream cuts the line into, so a Write of the whole line finds
// every one of them. Until the render succeeds, the audio built into the
// binary says them instead (ADR-0069).
type Canned struct {
	synth Synth

	mu    sync.RWMutex
	audio map[string][]byte
}

// Segments is text as a stream cuts it, each part synthesised on its own:
// the unit audio rendered ahead of time is kept in.
func Segments(text string) []string { return chunk(text) }

// FallbackName is the file under fallback/ holding a segment's audio as
// `task canned:voice` rendered it (ADR-0069).
func FallbackName(segment string) string {
	sum := sha256.Sum256([]byte(segment))
	return hex.EncodeToString(sum[:8]) + ".pcm"
}

// fallback is every canned segment rendered at build time, so a canned
// line can be said before the synthesiser has ever answered.
//
//go:embed fallback/*.pcm
var fallback embed.FS

// Fallback is a canned segment's audio built into the binary, read-only.
func Fallback(segment string) ([]byte, bool) {
	pcm, err := fallback.ReadFile("fallback/" + FallbackName(segment))
	return pcm, err == nil
}

// NewCanned wraps synth. Nothing is rendered until Render.
func NewCanned(synth Synth) *Canned {
	return &Canned{synth: synth, audio: map[string][]byte{}}
}

// Render renders every segment of lines not already kept, while the
// synthesiser answers. It stops at the first failure and keeps what it
// rendered, so calling it again finishes the rest.
func (c *Canned) Render(ctx context.Context, lines ...string) error {
	for _, line := range lines {
		for _, part := range chunk(line) {
			if _, ok := c.kept(part); ok {
				continue
			}
			pcm, err := c.synth.Synthesize(ctx, part)
			if err != nil {
				return fmt.Errorf("render %q: %w", part, err)
			}
			c.mu.Lock()
			c.audio[part] = pcm
			c.mu.Unlock()
		}
	}
	return nil
}

// Rendered reports whether every segment of lines is kept.
func (c *Canned) Rendered(lines ...string) bool {
	for _, line := range lines {
		for _, part := range chunk(line) {
			if _, ok := c.kept(part); !ok {
				return false
			}
		}
	}
	return true
}

// Synthesize answers a kept segment from its audio, whatever state the
// synthesiser is in; a canned segment not yet rendered from the audio built
// in, without waiting on a synthesiser that may be hung; and asks the
// synthesiser for anything else. The audio is shared, and read-only like
// every rendering the stream is handed.
func (c *Canned) Synthesize(ctx context.Context, text string) ([]byte, error) {
	if pcm, ok := c.kept(text); ok {
		return pcm, nil
	}
	if pcm, ok := Fallback(text); ok {
		return pcm, nil
	}
	return c.synth.Synthesize(ctx, text)
}

func (c *Canned) kept(text string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	pcm, ok := c.audio[text]
	return pcm, ok
}
