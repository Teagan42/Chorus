package session

import (
	"context"
	"errors"
)

// Speaker renders one utterance at a time. The stream shape exists because
// speech deltas go to TTS as emitted, not at end of message (SPEC §4.1).
type Speaker interface {
	Open(ctx context.Context, callID string) (Stream, error)
}

// Stream accepts deltas and reports what the DAC actually played.
type Stream interface {
	// Write enqueues a delta for playback.
	Write(text string) error

	// Close stops playback and reports the heard/unheard split. A cancelled
	// ctx must still yield the truncation point, not an error (SPEC §4.4).
	Close() Playback
}

// Starter is a Stream that can see its DAC. Started closes when the device
// first reports playing the utterance's audio. A stream that cannot see
// playback does not implement it, and its turns record no first frame rather
// than a guess (ADR-0035).
type Starter interface {
	Started() <-chan struct{}
}

// Playback is the truth about one utterance. The split comes from
// DAC-reported frames, not an estimate (SPEC §3.2.1).
type Playback struct {
	Spoken   string
	Unspoken string
	AudioRef string

	// Frames is the cumulative DAC frame count at completion or at the cut.
	Frames int64

	// Truncated reports that playback was cut short: by a barge-in, or by
	// the failure Failure names.
	Truncated bool

	// Failure is why playback stopped short when nobody cut it: it wraps
	// ErrVoiceUnavailable or ErrPlaybackUnconfirmed. Nil when the utterance
	// played out or was cut (ADR-0051).
	Failure error
}

// The two ways an utterance fails with nobody cutting it. Neither is the
// person interrupting, and neither may be recorded as one: a barge-in is a
// preference pair, and a dead synthesiser is not a preference (SPEC §9.1).
var (
	// ErrVoiceUnavailable is a synthesiser that would not open or render.
	ErrVoiceUnavailable = errors.New("tts_unavailable")
	// ErrPlaybackUnconfirmed is a device that stopped reporting playback
	// before the end of what it was sent.
	ErrPlaybackUnconfirmed = errors.New("playback_unconfirmed")
)
