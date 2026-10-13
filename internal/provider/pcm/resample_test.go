package pcm_test

import (
	"math"
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/provider/pcm"
)

func tone(hz float64, rate, n int, amp float64) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return out
}

// rms over the middle half, away from the edges where the kernel reads
// silence past the clause.
func rms(s []int16) float64 {
	mid := s[len(s)/4 : 3*len(s)/4]
	var acc float64
	for _, v := range mid {
		acc += float64(v) * float64(v)
	}
	return math.Sqrt(acc / float64(len(mid)))
}

func crossings(s []int16) int {
	mid := s[len(s)/4 : 3*len(s)/4]
	n := 0
	for i := 1; i < len(mid); i++ {
		if (mid[i-1] < 0) != (mid[i] < 0) {
			n++
		}
	}
	return n
}

// Every rate a voice server renders at reaches the device's with speech
// intact: the same duration, the same pitch, the same level.
func TestResamplerKeepsSpeechBandTones(t *testing.T) {
	for _, rate := range []int{8000, 11025, 22050, 24000, 44100, 48000} {
		r, err := pcm.NewResampler(rate)
		if err != nil {
			t.Fatalf("%d Hz: %v", rate, err)
		}
		in := tone(1000, rate, rate, 10000)
		out := r.Run(in)
		if len(out) != pcm.DeviceRate {
			t.Errorf("%d Hz: one second became %d samples, want %d", rate, len(out), pcm.DeviceRate)
		}
		// 1 kHz over half a second crosses zero ~1000 times.
		if c := crossings(out); c < 990 || c > 1010 {
			t.Errorf("%d Hz: %d zero crossings in half a second, want ~1000", rate, c)
		}
		if got, want := rms(out), rms(in); math.Abs(got-want)/want > 0.02 {
			t.Errorf("%d Hz: level %.0f, want %.0f", rate, got, want)
		}
	}
}

// Above the device's 8 kHz Nyquist nothing may fold back into the speech
// band, which is what a sibilant would otherwise do.
func TestResamplerStopsWhatWouldAlias(t *testing.T) {
	for _, rate := range []int{22050, 24000, 48000} {
		r, err := pcm.NewResampler(rate)
		if err != nil {
			t.Fatal(err)
		}
		out := r.Run(tone(10000, rate, rate, 10000))
		if db := 20 * math.Log10(rms(out)/(10000/math.Sqrt2)); db > -60 {
			t.Errorf("%d Hz: 10 kHz comes through at %.1f dB, want below -60", rate, db)
		}
	}
}

func TestResamplerPassesTheDeviceRateThrough(t *testing.T) {
	r, err := pcm.NewResampler(pcm.DeviceRate)
	if err != nil {
		t.Fatal(err)
	}
	in := []int16{1, -1, 32767, -32768}
	if out := r.Run(in); !slices.Equal(out, in) {
		t.Errorf("16 kHz changed: %v", out)
	}
}

func TestResamplerRefusesNonsenseRates(t *testing.T) {
	for _, rate := range []int{0, -1, 16001} {
		if _, err := pcm.NewResampler(rate); err == nil {
			t.Errorf("%d Hz accepted", rate)
		}
	}
}
