package kokoro

import "math"

// Kokoro renders at 24 kHz and its endpoint has no rate parameter, while the
// device is fixed at 16 kHz: the XMOS pipeline and micro_wake_word both run
// there and resampling on an ESP32 buys nothing (SPEC §3.2). So the host
// resamples, and 3:2 is exact -- output sample n sits at input position 1.5n,
// on a sample for even n and midway between two for odd. Two phases only, so
// both kernels are built once per synth.
const synthRate = 24000

const phases = 2

// halfTaps is each kernel's reach to either side. 33 taps measures -76 dB at
// 10 kHz, where a sibilant would otherwise fold back, and -0.2 dB at 6.5 kHz.
// Doubling it buys 8 dB of stopband and nothing audible.
const halfTaps = 16

// cutoffHz is the lowpass edge: just under the output's 8 kHz Nyquist, leaving
// a transition band a window of this width can actually deliver.
const cutoffHz = 7800.0

// cutoff is cutoffHz as the sinc's normalised argument -- twice the edge over
// the sample rate, not the edge over it. Halving this by mistake rolls speech
// off from 3.9 kHz and sounds muffled rather than broken.
const cutoff = 2 * cutoffHz / synthRate

// resampler holds the two phase kernels. Built per Synth rather than once at
// package level, so the table is not shared mutable state (CONTRIBUTING §6).
type resampler struct {
	kern [phases][]float64
}

func newResampler() resampler {
	var r resampler
	for p := range r.kern {
		taps := make([]float64, 2*halfTaps+1)
		// Odd outputs land half a sample late, so that phase's kernel is the
		// same sinc sampled off-centre.
		offset := float64(p) / phases
		var sum float64
		for i := range taps {
			t := float64(i-halfTaps) - offset
			taps[i] = cutoff * sinc(cutoff*t) * blackman(t)
			sum += taps[i]
		}
		// Normalised per phase. Unnormalised, the two phases have slightly
		// different DC gain and alternate samples come out at different levels,
		// which is audible as a 8 kHz buzz on steady vowels.
		for i := range taps {
			taps[i] /= sum
		}
		r.kern[p] = taps
	}
	return r
}

// run converts 24 kHz mono samples to 16 kHz. Input past either end counts as
// silence: every clause is synthesised on its own and starts and ends near
// zero, so there is nothing to carry across.
func (r resampler) run(in []int16) []int16 {
	out := make([]int16, len(in)*2/3)
	for n := range out {
		base := 3 * n / 2
		taps := r.kern[n%phases]
		var acc float64
		for j, tap := range taps {
			if i := base + j - halfTaps; i >= 0 && i < len(in) {
				acc += tap * float64(in[i])
			}
		}
		out[n] = clamp(acc)
	}
	return out
}

// clamp saturates rather than wrapping. The lowpass overshoots on a transient,
// and a wrapped sample is a click at full scale.
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

func blackman(t float64) float64 {
	if math.Abs(t) > halfTaps {
		return 0
	}
	x := math.Pi * t / halfTaps
	return 0.42 + 0.5*math.Cos(x) + 0.08*math.Cos(2*x)
}
