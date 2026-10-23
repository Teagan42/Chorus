// Package echotest is a room for host-side tests: the path from a
// satellite's speaker back to its uplink, so a test can hear the device's own
// voice come back through its microphone without a device or a kitchen.
//
// The voices are the review UI's household (internal/reviewui/household):
// the assistant's answers in its own Kokoro voice and each person's asks in
// theirs. Synthetic, but speech, which is the point -- the echo stage judges
// the shape of a voice, and a sine or a constant has none (CONTRIBUTING §1).
package echotest

import (
	"context"
	"encoding/binary"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// Rate is every clip's and every room's sample rate: the device's 16 kHz.
const Rate = household.DeviceRate

var (
	clipsOnce sync.Once
	clips     *blob.Memory
	clipsErr  error
)

// Clip is one of the household's recordings, by its ref without the scheme:
// "tts/jazz-playlist" is the assistant, "mic/jazz-bargein" is Alan.
func Clip(t testing.TB, ref string) []byte {
	t.Helper()
	clipsOnce.Do(func() { clips, clipsErr = household.Blobs(context.Background()) })
	if clipsErr != nil {
		t.Fatal(clipsErr)
	}
	pcm, ok := clips.Bytes(blob.RefFor(ref))
	if !ok {
		t.Fatalf("the household has no clip %s", ref)
	}
	return pcm
}

// Room is the path from the satellite's speaker to the uplink's processed
// channel, as ADR-0089 measured its result: a small loudspeaker driven into
// its knee, a room's reflections, the XMOS echo canceller taking out what it
// modelled and leaving what it did not, and an AGC bringing what is left up
// to where a person's voice lands.
type Room struct {
	// Delay is how long after the DAC reports a frame its echo reaches the
	// uplink, in frames: capture buffering and send batching.
	Delay int
	// Misadjust is the canceller's error, as the fraction of the room's
	// response it failed to model.
	Misadjust float64
	// Drive is how hard the speaker's amplifier is pushed into its knee:
	// the part of the echo no linear canceller can model.
	Drive float64
	// Leak is the residual's level against the played audio's, in dB: how
	// loud the device's own voice is beside a person speaking at the
	// speaker's level.
	Leak float64
	// Level is the AGC's target RMS, a fraction of full scale.
	Level float64
	// Noise is the room's floor after processing, a fraction of full scale.
	Noise float64
	Seed  uint64
}

// Kitchen is the room ADR-0096's numbers were measured in: a residual 10 dB
// under a person, arriving 140 ms after the DAC reports it, brought up by the
// AGC to -19 dBFS -- where ADR-0089 measured both the echo and a household's
// voice on a real Voice PE.
var Kitchen = Room{Delay: 140 * Rate / 1000, Misadjust: 0.12, Drive: 2.5, Leak: -10, Level: 0.11, Noise: 0.001, Seed: 7}

// Echo is the uplink while the device plays played and nobody speaks, indexed
// so frame i arrived while the DAC reported frame i of played. It runs Delay
// past the end of played, as the tail of the echo does.
func (r Room) Echo(played []byte) []byte {
	return PCM(r.Uplink(r.Residual(Samples(played)), nil, 0))
}

// Over is the uplink while the device plays played and person speaks into the
// room from frame at of it.
func (r Room) Over(played, person []byte, at int) []byte {
	return PCM(r.Uplink(r.Residual(Samples(played)), Samples(person), at))
}

// Residual is what is left of played on the uplink once the echo canceller
// has taken out what it modelled, before the AGC, indexed as Echo is.
func (r Room) Residual(played []float64) []float64 {
	driven := make([]float64, len(played))
	for i, v := range played {
		driven[i] = math.Tanh(r.Drive*v) / r.Drive
	}
	h := r.response()
	// The canceller's model of the same room, off by Misadjust.
	hm := r.response()
	rng := rand.New(rand.NewPCG(r.Seed+1, r.Seed+2))
	for i := range hm {
		hm[i] *= 1 + r.Misadjust*rng.NormFloat64()
	}
	// It cancels the linear echo of what it was told was played, which is
	// not quite what the speaker made of it.
	heard, model := convolve(driven, h), convolve(played, hm)
	out := make([]float64, len(played)+r.Delay)
	for i := range heard {
		out[i+r.Delay] = heard[i] - model[i]
	}
	g := math.Pow(10, r.Leak/20) * rms(played) / rms(out)
	for i := range out {
		out[i] *= g
	}
	return out
}

// Uplink is the processed channel: the residual, with person (nil for
// nobody) speaking into the room from frame at, through the AGC and over the
// room's floor.
func (r Room) Uplink(residual, person []float64, at int) []float64 {
	n := len(residual)
	if person != nil {
		n = max(n, at+len(person))
	}
	mic := make([]float64, n)
	copy(mic, residual)
	for i, v := range person {
		mic[at+i] += v
	}
	agc(mic, r.Level)
	noise := rand.New(rand.NewPCG(r.Seed+3, r.Seed+4))
	for i := range mic {
		mic[i] += noise.NormFloat64() * r.Noise
	}
	return mic
}

// response is a room impulse response: the direct path, then reflections
// decaying over about 150 ms.
func (r Room) response() []float64 {
	rng := rand.New(rand.NewPCG(r.Seed, r.Seed^0x9e3779b97f4a7c15))
	h := make([]float64, 2400)
	h[16] = 1
	for i := 40; i < len(h); i++ {
		h[i] = rng.NormFloat64() * 0.3 * math.Exp(-float64(i)/400)
	}
	return h
}

// convolve is x through h, as long as x, by FFT: a room's response is
// thousands of taps and a household's day is minutes of audio.
func convolve(x, h []float64) []float64 {
	n := 1
	for n < len(x)+len(h) {
		n <<= 1
	}
	a, b := make([]complex128, n), make([]complex128, n)
	for i, v := range x {
		a[i] = complex(v, 0)
	}
	for i, v := range h {
		b[i] = complex(v, 0)
	}
	fft(a, false)
	fft(b, false)
	for i := range a {
		a[i] *= b[i]
	}
	fft(a, true)
	y := make([]float64, len(x))
	for i := range y {
		y[i] = real(a[i]) / float64(n)
	}
	return y
}

// fft is an in-place radix-2 transform, inverse without the 1/n.
func fft(a []complex128, inverse bool) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	sign := -1.0
	if inverse {
		sign = 1
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, sign*2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := range size / 2 {
				u, v := a[start+k], a[start+k+size/2]*wk
				a[start+k], a[start+k+size/2] = u+v, u-v
				wk *= w
			}
		}
	}
}

// agc brings each 20 ms block toward level, with a gain that rises slowly
// and falls fast, as the XMOS AGC does; it never boosts past 30 dB.
func agc(x []float64, level float64) {
	const block = Rate / 50
	gain := 1.0
	for s := 0; s < len(x); s += block {
		e := min(s+block, len(x))
		r := rms(x[s:e])
		want := 31.6
		if r > 1e-6 {
			want = min(level/r, want)
		}
		if want < gain {
			gain = want
		} else {
			gain += (want - gain) * 0.05
		}
		for i := s; i < e; i++ {
			x[i] = max(min(x[i]*gain, 1), -1)
		}
	}
}

func rms(x []float64) float64 {
	var ss float64
	for _, v := range x {
		ss += v * v
	}
	return math.Sqrt(ss/float64(max(len(x), 1))) + 1e-12
}

// Samples reads 16-bit little-endian PCM as fractions of full scale.
func Samples(pcm []byte) []float64 {
	x := make([]float64, len(pcm)/2)
	for i := range x {
		x[i] = float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	return x
}

// PCM writes fractions of full scale as 16-bit little-endian PCM, clipped.
func PCM(x []float64) []byte {
	b := make([]byte, 2*len(x))
	for i, v := range x {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(int16(max(min(v*32767, 32767), -32768))))
	}
	return b
}
