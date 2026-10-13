package identity

import (
	"encoding/binary"
	"math"

	"github.com/teagan42/chorus/internal/bridge"
)

// A satellite's own recording is mostly room: a 30 s wake clip measured
// 2026-10-13 held one 4 s sentence, and TitaNet pools over everything it is
// given, so the silence outvoted the voice. Enrollment from such a clip trims
// to the voiced span first. An uploaded take is never trimmed -- it is the
// household's own file, held to the format and otherwise left alone
// (ADR-0080).

// trimWindow is the frame the voiced span is found in, in samples. 20 ms is
// shorter than a syllable and long enough for a stable RMS.
const trimWindow = bridge.SampleRate / 50

// trimPad is how much room stays either side of the voiced span, in samples.
// A word's onset carries identity and sits below the threshold.
const trimPad = bridge.SampleRate / 10

// trimFloor is how far below the loudest window a window may be and still
// count as voice. -18 dB keeps an utterance's quiet tail and drops a room.
const trimFloor = 0.125

// MinVoicedSamples is the shortest voiced span worth embedding: 3.5 s.
// Measured on a Voice PE 2026-10-13. Two 1 s slices of one clean voice
// scored 0.499 against each other, under the 0.70 DefaultAccept; a 2.8 s
// clip of an enrolled household voice scored 0.268 against their own
// voiceprint, while the takes that built a usable one ran 3.2 to 4.4 s. So
// the floor is above the longest take that failed, not above the shortest
// that worked.
const MinVoicedSamples = 7 * bridge.SampleRate / 2

// TrimSilence returns the voiced span of a satellite's recording, padded, or
// the whole of it when nothing stands out of the noise. The caller decides
// whether what comes back is enough to embed; see MinVoicedSamples.
func TrimSilence(pcm []byte) []byte {
	samples := len(pcm) / 2
	if samples < trimWindow {
		return pcm
	}
	windows := samples / trimWindow
	loudest := 0.0
	rms := make([]float64, windows)
	for w := range windows {
		var sum float64
		for i := range trimWindow {
			s := float64(int16(binary.LittleEndian.Uint16(pcm[2*(w*trimWindow+i):])))
			sum += s * s
		}
		rms[w] = math.Sqrt(sum / trimWindow)
		loudest = max(loudest, rms[w])
	}
	if loudest == 0 {
		return pcm
	}
	first, last := -1, -1
	for w, r := range rms {
		if r < loudest*trimFloor {
			continue
		}
		if first < 0 {
			first = w
		}
		last = w
	}
	if first < 0 {
		return pcm
	}
	from := max(0, first*trimWindow-trimPad)
	to := min(samples, (last+1)*trimWindow+trimPad)
	return pcm[2*from : 2*to]
}
