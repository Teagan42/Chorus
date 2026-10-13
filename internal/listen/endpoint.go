package listen

import (
	"encoding/binary"
	"math"

	"github.com/teagan42/chorus/internal/bridge"
)

const bytesPerFrame = bridge.BitsPerSample / 8

// bytesPerSecond is one second of device audio, the unit every bound in this
// package is stated in (stt.DefaultPartialEvery does the same).
const bytesPerSecond = bridge.SampleRate * bytesPerFrame

// Boundary is what an Endpointer saw in one chunk.
type Boundary uint8

const (
	// Continue: nothing changed. The chunk belongs to whatever is open.
	Continue Boundary = iota
	// Start: speech began. The chunk is the utterance's first.
	Start
	// End: the utterance ended. The chunk is its last.
	End
)

func (b Boundary) String() string {
	switch b {
	case Continue:
		return "continue"
	case Start:
		return "start"
	case End:
		return "end"
	default:
		return "boundary(?)"
	}
}

// Endpointer segments the continuous mic stream into utterances. Energy is
// the default; Semantic asks Smart Turn (SPEC §4.5, ADR-0036). Feed is called
// from the bridge read loop with every chunk in arrival order, so an
// implementation that runs a model must do so off that loop and report the
// decision on a later Feed.
type Endpointer interface {
	Feed(pcm []byte) Boundary

	// Reset forgets any utterance in progress, as a mute does.
	Reset()
}

// Trailer is an Endpointer that can say how much quiet its last End waited
// through, in bytes. The person stopped speaking that long before the End,
// which is where the wait for an answer starts (ADR-0035). An Endpointer
// that cannot say is taken to have ended on the last word.
type Trailer interface {
	Trailing() int
}

// DefaultSpeechEnergy is the RMS, as a fraction of full scale, at or above
// which a chunk counts as speech: -40 dBFS. The XMOS AGC lands speech well
// above it and the AEC residual during playback well below. Unmeasured on a
// real room; the vad-stage rejections in the journal are what tunes it.
const DefaultSpeechEnergy = 0.01

// DefaultSilence is the trailing quiet that ends an utterance: 800 ms, in
// bytes. Long enough to sit inside "turn off the... kitchen lights" without
// cutting, short enough that the turn does not feel late (SPEC §11). A
// semantic endpointer replaces this guess rather than tuning it.
const DefaultSilence = 800 * bytesPerSecond / 1000

// Energy is the first Endpointer: speech is loud, silence is quiet, and an
// utterance ends after enough quiet. It knows nothing about words, which is
// exactly what SPEC §4.5's semantic endpointing is for; its limits are the
// ADR-0030 record.
type Energy struct {
	// Threshold is the RMS fraction of full scale that counts as speech.
	Threshold float64

	// Silence is how many bytes of consecutive quiet end an utterance.
	Silence int

	voiced bool
	quiet  int
	trail  int
}

// NewEnergy returns the defaults.
func NewEnergy() *Energy {
	return &Energy{Threshold: DefaultSpeechEnergy, Silence: DefaultSilence}
}

func (e *Energy) Feed(pcm []byte) Boundary {
	loud := RMS(pcm) >= e.Threshold
	if !e.voiced {
		if !loud {
			return Continue
		}
		e.voiced, e.quiet = true, 0
		return Start
	}
	if loud {
		e.quiet = 0
		return Continue
	}
	e.quiet += len(pcm)
	if e.quiet < e.Silence {
		return Continue
	}
	e.voiced, e.quiet, e.trail = false, 0, e.quiet
	return End
}

func (e *Energy) Reset() { e.voiced, e.quiet = false, 0 }

// Trailing is the quiet the last End waited through, in bytes.
func (e *Energy) Trailing() int { return e.trail }

// RMS is a chunk's level as a fraction of full scale, so Candidate.Energy and
// session.Gate.MinEnergy share a unit. A torn trailing byte is ignored.
func RMS(pcm []byte) float64 { return rms(sumSquares(pcm), len(pcm)/bytesPerFrame) }

// sumSquares is the energy of the whole samples in a chunk.
func sumSquares(pcm []byte) float64 {
	var sum float64
	for i := 0; i+bytesPerFrame <= len(pcm); i += bytesPerFrame {
		s := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
		sum += s * s
	}
	return sum
}

func rms(sumsq float64, samples int) float64 {
	if samples == 0 {
		return 0
	}
	return math.Sqrt(sumsq/float64(samples)) / -math.MinInt16
}

// sustainWindow is the frame sustained speech is counted in, in bytes. 20 ms,
// as the trim uses: shorter than a syllable, long enough for a stable RMS.
const sustainWindow = 20 * bytesPerSecond / 1000

// DefaultMinSustained is how much of an utterance must hold speech-level
// energy before its words are believed, in bytes. 200 ms.
//
// A mouse click is loud and empty: measured 2026-10-13, three clicks a
// household never spoke peaked at -8 to -14 dBFS and held speech level for
// 20, 40 and 120 ms, with a crest factor of 24 to 26 dB. The ear turned each
// into a word -- "Yeah.", "No." -- and each became a turn, and one of them
// could have answered a held confirmation. Real speech from the same room
// held 740 to 2520 ms at a crest factor of 14 to 20 dB. So the floor sits
// above the longest click and far below the shortest sentence (ADR-0086).
const DefaultMinSustained = 200 * bytesPerSecond / 1000

// Sustained is how much of pcm holds energy at or above threshold, in bytes,
// counted in whole windows.
func Sustained(pcm []byte, threshold float64) int {
	kept := 0
	for i := 0; i+sustainWindow <= len(pcm); i += sustainWindow {
		if RMS(pcm[i:i+sustainWindow]) >= threshold {
			kept += sustainWindow
		}
	}
	return kept
}
