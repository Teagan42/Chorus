// Package echo tells the device's own voice, coming back through its
// microphone, from a person talking over it. The host knows exactly what it
// sent the speaker and where the DAC was when it played (SPEC §3.2.1), so it
// keeps that audio as a reference and asks whether the mic is explained by it
// (ADR-0096).
package echo

import (
	"encoding/binary"
	"sync"
)

// DefaultKeep is how much played audio a Reference holds: long enough to
// cover the longest candidate judged plus the widest lag searched.
const DefaultKeep = 12 * rate

// Reference is what the satellite was sent to play, placed on the DAC's
// cumulative frame count: the same count PLAYED reports and a candidate's
// position is measured on. The Speaking side writes it, the Listening side
// reads it; both may run at once.
type Reference struct {
	mu    sync.Mutex
	keep  int
	start uint64  // DAC frame of pcm[0]
	pcm   []int16 // contiguous from start; zeros where nothing was sent
}

// NewReference keeps the newest keep frames; zero or less takes DefaultKeep.
func NewReference(keep int) *Reference {
	if keep <= 0 {
		keep = DefaultKeep
	}
	return &Reference{keep: keep}
}

// Write places pcm, 16-bit little-endian mono, from DAC frame at. Anything
// already held from at onwards is dropped first: a barge-in discards the
// device's buffer, so the next utterance starts where the cut landed and the
// audio that was never played is not what the room heard.
func (r *Reference) Write(at uint64, pcm []byte) {
	if r == nil || len(pcm) < 2 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	end := r.start + uint64(len(r.pcm))
	switch {
	case len(r.pcm) == 0 || at < r.start || (at > end && at-end > uint64(r.keep)):
		// Empty, rewound behind what is held, or far enough ahead that the
		// gap would be all silence: start over.
		r.start, r.pcm = at, r.pcm[:0]
	case at < end:
		r.pcm = r.pcm[:at-r.start]
	default:
		for range at - end {
			r.pcm = append(r.pcm, 0)
		}
	}
	for i := 0; i+1 < len(pcm); i += 2 {
		r.pcm = append(r.pcm, int16(binary.LittleEndian.Uint16(pcm[i:])))
	}
	if extra := len(r.pcm) - r.keep; extra > 0 {
		r.pcm = append(r.pcm[:0], r.pcm[extra:]...)
		r.start += uint64(extra)
	}
}

// Cut drops everything held from DAC frame at onwards: what a stop kept the
// room from hearing.
func (r *Reference) Cut(at uint64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if at < r.start {
		r.pcm = r.pcm[:0]
		return
	}
	if n := at - r.start; n < uint64(len(r.pcm)) {
		r.pcm = r.pcm[:n]
	}
}

// span is the reference from DAC frame from, n frames long, as fractions of
// full scale, with silence wherever nothing is held.
func (r *Reference) span(from int64, n int) []float64 {
	out := make([]float64, n)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range out {
		at := from + int64(i)
		if at < int64(r.start) {
			continue
		}
		j := at - int64(r.start)
		if j >= int64(len(r.pcm)) {
			break
		}
		out[i] = float64(r.pcm[j]) / 32768
	}
	return out
}
