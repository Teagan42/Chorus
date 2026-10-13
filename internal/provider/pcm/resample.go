package pcm

import (
	"fmt"
	"math"
)

// baseHalfTaps is each kernel's reach to either side when the output rate is
// at least the input's: 33 taps, the width Kokoro's 3:2 resampler measured
// at -76 dB where a sibilant would fold back (ADR-0023). Downsampling by more
// than that widens it in proportion, so the transition band stays the same
// width at the output.
const baseHalfTaps = 16

// edge is the lowpass edge as a fraction of the lower rate's Nyquist: just
// under it, leaving a transition band a window this wide can deliver.
const edge = 0.975

// maxPhases bounds the kernel table. Every rate a voice actually renders at
// (8, 11.025, 22.05, 24, 44.1, 48 kHz) needs at most 640 phases to reach
// 16 kHz; a rate needing more is a misreported header, not a voice.
const maxPhases = 4096

// Resampler converts mono samples at one rate to DeviceRate with a polyphase
// windowed sinc: output n sits at input position n*down/up, and each of the
// up positions between two input samples has its own kernel, built once.
type Resampler struct {
	up, down int
	half     int
	kern     [][]float64
}

// NewResampler builds the kernels for audio at rate.
func NewResampler(rate int) (*Resampler, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("resample: rate %d Hz", rate)
	}
	g := gcd(rate, DeviceRate)
	r := &Resampler{up: DeviceRate / g, down: rate / g}
	if r.up == r.down {
		return r, nil
	}
	if r.up > maxPhases {
		return nil, fmt.Errorf("resample: %d Hz to %d Hz needs %d phases", rate, DeviceRate, r.up)
	}
	ratio := 1.0
	if rate > DeviceRate {
		ratio = float64(DeviceRate) / float64(rate)
	}
	r.half = int(math.Ceil(baseHalfTaps / ratio))
	// The sinc's normalised argument is twice the edge over the input rate.
	cutoff := edge * ratio
	r.kern = make([][]float64, r.up)
	for p := range r.kern {
		taps := make([]float64, 2*r.half+1)
		offset := float64(p) / float64(r.up)
		var sum float64
		for i := range taps {
			t := float64(i-r.half) - offset
			taps[i] = cutoff * sinc(cutoff*t) * blackman(t, float64(r.half))
			sum += taps[i]
		}
		// Normalised per phase, or the phases differ in DC gain and a steady
		// vowel picks up a buzz at the phase rate.
		for i := range taps {
			taps[i] /= sum
		}
		r.kern[p] = taps
	}
	return r, nil
}

// Run converts one clause. Input past either end counts as silence: each
// clause is rendered on its own and starts and ends near zero.
func (r *Resampler) Run(in []int16) []int16 {
	if r.up == r.down {
		return append([]int16(nil), in...)
	}
	out := make([]int16, len(in)*r.up/r.down)
	for n := range out {
		pos := n * r.down
		base, phase := pos/r.up, pos%r.up
		taps := r.kern[phase]
		var acc float64
		for j, tap := range taps {
			if i := base + j - r.half; i >= 0 && i < len(in) {
				acc += tap * float64(in[i])
			}
		}
		out[n] = clamp(acc)
	}
	return out
}

// clamp saturates rather than wrapping: the lowpass overshoots on a
// transient, and a wrapped sample is a click at full scale.
func clamp(v float64) int16 {
	switch {
	case v > math.MaxInt16:
		return math.MaxInt16
	case v < math.MinInt16:
		return math.MinInt16
	}
	return int16(math.Round(v))
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func blackman(t, half float64) float64 {
	if math.Abs(t) > half {
		return 0
	}
	x := math.Pi * t / half
	return 0.42 + 0.5*math.Cos(x) + 0.08*math.Cos(2*x)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
