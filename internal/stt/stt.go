// Package stt is the hearing end of the cascade: the seam a speech-to-text
// provider fills, and the utterance stream that turns a batch endpoint into
// the partials the barge-in gate and endpointing read (SPEC §4.3, §4.5, §10).
package stt

import (
	"context"

	"github.com/teaganglenn/chorus/internal/bridge"
)

// Transcriber decodes one complete utterance. The audio is the device's own
// format -- 16 kHz, signed 16-bit, mono, little-endian (internal/bridge) --
// so the provider, not the caller, owns whatever container its endpoint wants.
type Transcriber interface {
	Transcribe(ctx context.Context, pcm []byte) (Result, error)
}

// Result is what the endpoint gave back. Text only, deliberately: the pinned
// Parakeet path answers with nothing else (ADR-0024), and a field no endpoint
// fills is a field a consumer learns to rely on.
type Result struct {
	Text string
}

const bytesPerFrame = bridge.BitsPerSample / 8

// bytesPerSecond is one second of device audio, the unit the bounds below are
// stated in.
const bytesPerSecond = bridge.SampleRate * bytesPerFrame

// DefaultPartialEvery is how much new audio lands between re-decodes. Half a
// second, because the gate's partial-length stage needs two words (SPEC §4.3)
// and two words do not exist before roughly that much speech: a tighter
// cadence re-decodes a growing buffer for a partial that cannot pass yet.
// Unmeasured against a real sidecar; the models tier reports what a decode
// costs so this can be tuned.
const DefaultPartialEvery = bytesPerSecond / 2

// DefaultMaxUtterance bounds what one utterance may hold. Re-decoding a growing
// buffer is quadratic in its length, and a session's silence backstop is ~20 s
// (SPEC §4.5): anything longer is a stuck mic or the television, not a turn.
// Parakeet itself takes 24 minutes in one pass, so the model is not the limit.
const DefaultMaxUtterance = 30 * bytesPerSecond
