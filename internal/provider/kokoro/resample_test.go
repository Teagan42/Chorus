package kokoro

import (
	"math"
	"testing"

	"github.com/teaganglenn/chorus/internal/bridge"
)

// tone is amplitude*sin at hz, sampled at synthRate for d seconds.
func tone(hz, amplitude float64, samples int) []int16 {
	out := make([]int16, samples)
	for i := range out {
		out[i] = int16(amplitude * math.Sin(2*math.Pi*hz*float64(i)/synthRate))
	}
	return out
}

// amplitudeAt measures the peak amplitude of the component at hz, by
// correlation. Phase is unknown after a resample, so both quadratures are
// needed.
func amplitudeAt(s []int16, hz float64) float64 {
	var re, im float64
	for i, v := range s {
		p := 2 * math.Pi * hz * float64(i) / bridge.SampleRate
		re += float64(v) * math.Cos(p)
		im += float64(v) * math.Sin(p)
	}
	n := float64(len(s))
	return 2 * math.Sqrt(re*re+im*im) / n
}

func TestTheOutputIsTwoThirdsAsManySamples(t *testing.T) {
	r := newResampler()
	for _, n := range []int{0, 1, 3, 300, 35596} {
		if got, want := len(r.run(tone(1000, 8000, n))), n*2/3; got != want {
			t.Errorf("%d samples in -> %d out, want %d", n, got, want)
		}
	}
}

// A tone inside the band has to survive at its own frequency and level: this is
// the whole job, and a resampler that halves the volume or shifts the pitch
// passes every length check.
func TestAToneInBandKeepsItsPitchAndLevel(t *testing.T) {
	r := newResampler()
	const amplitude = 8000
	for _, hz := range []float64{200, 1000, 3000} {
		// Edges are windowed against silence, so the measurement skips them.
		out := r.run(tone(hz, amplitude, 24000))[200 : 16000-200]
		got := amplitudeAt(out, hz)
		if math.Abs(got-amplitude)/amplitude > 0.02 {
			t.Errorf("%g Hz came out at amplitude %.0f, want ~%d", hz, got, amplitude)
		}
	}
}

// The reason this is a filtered resample and not a decimation. Dropping every
// third sample folds anything above 8 kHz back into the band -- a 10 kHz
// sibilant lands at 6 kHz, in the middle of speech, as a whistle.
//
// verifies SPEC §3.2
func TestAToneAboveTheOutputNyquistDoesNotFoldBack(t *testing.T) {
	r := newResampler()
	out := r.run(tone(10000, 8000, 24000))[200 : 16000-200]

	// Decimating by 3:2 reflects 10 kHz about the 8 kHz output Nyquist, to
	// 6 kHz -- in the middle of the band, where nothing can remove it.
	alias := amplitudeAt(out, 6000)
	if alias > 80 { // 1% of the input amplitude, -40 dB
		t.Errorf("10 kHz folded back to 6 kHz at amplitude %.0f", alias)
	}
}

func TestSilenceStaysSilent(t *testing.T) {
	out := newResampler().run(make([]int16, 3000))
	for i, v := range out {
		if v != 0 {
			t.Fatalf("sample %d of silence is %d", i, v)
		}
	}
}

// Full-scale audio plus the filter's overshoot exceeds int16, and a wrapped
// sample is a click at full scale in the opposite direction.
func TestOvershootSaturatesRatherThanWrapping(t *testing.T) {
	cases := []struct {
		in   float64
		want int16
	}{
		{40000, math.MaxInt16},
		{-40000, math.MinInt16},
		{1.6, 2},
		{-1.6, -2},
	}
	for _, c := range cases {
		if got := clamp(c.in); got != c.want {
			t.Errorf("clamp(%g) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDCPassesThroughAtUnityGain(t *testing.T) {
	in := make([]int16, 3000)
	for i := range in {
		in[i] = 1000
	}
	out := newResampler().run(in)[200 : 2000-200]
	for i, v := range out {
		if v != 1000 {
			t.Fatalf("sample %d of a constant 1000 is %d", i, v)
		}
	}
}
