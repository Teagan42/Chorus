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

// Pauser is an Endpointer that can say the speech it holds has paused for
// long enough to be worth answering, though the utterance has not ended.
// Paused numbers the pause, so a pause spoken through and a later one are
// told apart; paused is false while speech goes on (ADR-0095).
type Pauser interface {
	Paused() (n uint64, paused bool)
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

	// Pause is how many bytes of quiet make a pause, as Semantic asks its
	// judge after. Zero reports none.
	Pause int

	voiced bool
	quiet  int
	trail  int
	pauses uint64
}

// NewEnergy returns the defaults.
func NewEnergy() *Energy {
	return &Energy{Threshold: DefaultSpeechEnergy, Silence: DefaultSilence, Pause: DefaultPause}
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
		if e.quiet > 0 {
			e.pauses++
		}
		e.quiet = 0
		return Continue
	}
	e.quiet += len(pcm)
	if e.quiet < e.Silence {
		return Continue
	}
	e.voiced, e.quiet, e.trail = false, 0, e.quiet
	e.pauses++
	return End
}

func (e *Energy) Reset() { e.voiced, e.quiet, e.pauses = false, 0, e.pauses+1 }

// Paused is the current pause, once the quiet has reached Pause.
func (e *Energy) Paused() (uint64, bool) {
	return e.pauses, e.Pause > 0 && e.voiced && e.quiet >= e.Pause
}

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

// DefaultMinSustained is how little speech-level energy makes an utterance
// suspect, in bytes: 200 ms. On its own it is not a reason to disbelieve the
// words -- a quiet one-word answer holds 40 ms of it, as a click does -- so
// it is only half the test (ADR-0090).
const DefaultMinSustained = 200 * bytesPerSecond / 1000

// DefaultMaxCrest is the peak-to-RMS ratio in dB above which audio is an
// impulse rather than a voice.
//
// Measured 2026-10-13. Three mouse clicks the household never spoke: 24.3,
// 24.9 and 25.5 dB. Five real utterances from the same room, loud and quiet,
// one word and several: 13.7, 14.5, 16.2, 17.3 and 20.4 dB. A click is a
// spike in a quiet room and a voice is not, whatever either one says, and
// nothing else measured separated them -- sustained duration put a quiet
// "I don't know" at 40 ms, among the clicks (ADR-0090).
const DefaultMaxCrest = 22.0

// Impulsive reports audio shaped like a transient rather than a voice: a
// peak far above its own average, holding speech level too briefly to be a
// word. Both, because either alone catches real speech. maxCrest is the
// satellite's own ceiling in dB; zero takes DefaultMaxCrest (ADR-0098).
func Impulsive(pcm []byte, threshold float64, minSustained int, maxCrest float64) bool {
	if maxCrest == 0 {
		maxCrest = DefaultMaxCrest
	}
	if len(pcm) < bytesPerFrame {
		return false
	}
	if Sustained(pcm, threshold) >= minSustained {
		return false
	}
	mean := RMS(pcm)
	if mean <= 0 {
		return false
	}
	return 20*math.Log10(peak(pcm)/mean) > maxCrest
}

// peak is the largest absolute sample in pcm, as a fraction of full scale.
func peak(pcm []byte) float64 {
	var most int16
	for i := 0; i+bytesPerFrame <= len(pcm); i += bytesPerFrame {
		s := int16(binary.LittleEndian.Uint16(pcm[i:]))
		if s < 0 {
			s = -s
		}
		if s > most {
			most = s
		}
	}
	return float64(most) / 32768
}

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
