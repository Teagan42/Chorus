package echo

import (
	"encoding/binary"
	"math"
	"math/cmplx"
)

// The spectrogram the comparison runs on: 32 ms frames every 10 ms, each
// summed into 24 log-spaced bands across the speech range, and each row
// taken relative to its own mean -- the spectrum's shape, not its loudness.
//
// Bands and not samples, because the device's own voice reaches the uplink
// through a loudspeaker, a room, the XMOS echo canceller, noise suppression
// and an AGC: what survives all of that is which bands carry the voice when,
// not the waveform. Shape and not loudness, because every voice rises and
// falls with its syllables, so two unrelated ones line up on loudness by
// chance; measured on the household's voices, shape halved how often a
// person scored as an echo (ADR-0096).
const (
	frameLen = 512 // 32 ms at 16 kHz, a power of two for the FFT
	hop      = 160 // 10 ms
	bands    = 24
	loHz     = 150
	hiHz     = 6000
	rate     = 16000

	// floorPower keeps digital silence in the reference from dominating a
	// band's log: -70 dBFS of a full-scale frame. A room's mic never gets
	// that quiet, so a quiet reference frame and a quiet mic frame meet here.
	floorPower = 1e-7
)

// edges are each band's FFT bins, log-spaced from loHz to hiHz. Every band
// holds at least one bin at this frame length.
var edges = func() [bands + 1]int {
	var e [bands + 1]int
	for i := range e {
		hz := loHz * math.Pow(hiHz/loHz, float64(i)/bands)
		e[i] = int(math.Round(hz * frameLen / rate))
	}
	return e
}()

var window = func() [frameLen]float64 {
	var w [frameLen]float64
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/frameLen)
	}
	return w
}()

// spectrogram is one row of band log-powers per hop, each relative to the
// row's mean.
type spectrogram [][bands]float64

// levels is each full frame's RMS, a fraction of full scale, on the same
// rows as featurize.
func levels(x []float64) []float64 {
	if len(x) < frameLen {
		return nil
	}
	out := make([]float64, 1+(len(x)-frameLen)/hop)
	for t := range out {
		var ss float64
		for _, v := range x[t*hop : t*hop+frameLen] {
			ss += v * v
		}
		out[t] = math.Sqrt(ss / frameLen)
	}
	return out
}

// featurize turns samples, as fractions of full scale, into a spectrogram,
// one row per full frame. Frames are real, so two ride each transform: one
// as the real part and one as the imaginary.
func featurize(x []float64) spectrogram {
	if len(x) < frameLen {
		return nil
	}
	n := 1 + (len(x)-frameLen)/hop
	out := make(spectrogram, n)
	buf := make([]complex128, frameLen)
	for t := 0; t < n; t += 2 {
		f, g := x[t*hop:t*hop+frameLen], []float64(nil)
		if t+1 < n {
			g = x[(t+1)*hop : (t+1)*hop+frameLen]
		}
		for i := range buf {
			var im float64
			if g != nil {
				im = g[i] * window[i]
			}
			buf[i] = complex(f[i]*window[i], im)
		}
		fft(buf)
		row(&out[t], buf, 1)
		if g != nil {
			row(&out[t+1], buf, -1)
		}
	}
	return out
}

// row fills one spectrogram row from a transform of two real frames: the
// real part's spectrum when part is 1, the imaginary part's when it is -1.
// X[k] and conj(X[N-k]) sum to twice the first and differ by 2i times the
// second, so either power is |X[k] ± conj(X[N-k])|² / 4.
func row(dst *[bands]float64, buf []complex128, part float64) {
	var mean float64
	for b := range bands {
		var p float64
		for k := edges[b]; k < edges[b+1]; k++ {
			c := cmplx.Conj(buf[(frameLen-k)%frameLen])
			v := buf[k] + complex(part, 0)*c
			p += (real(v)*real(v) + imag(v)*imag(v)) / 4
		}
		dst[b] = math.Log10(p/frameLen + floorPower)
		mean += dst[b]
	}
	mean /= bands
	for b := range bands {
		dst[b] -= mean
	}
}

// samples reads 16-bit little-endian PCM as fractions of full scale.
func samples(pcm []byte) []float64 {
	x := make([]float64, len(pcm)/2)
	for i := range x {
		x[i] = float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
	}
	return x
}

// twiddle is e^(-2πik/frameLen) for the first half of the frame.
var twiddle = func() [frameLen / 2]complex128 {
	var w [frameLen / 2]complex128
	for k := range w {
		w[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/frameLen))
	}
	return w
}()

// fft is an in-place radix-2 transform of one frame.
func fft(a []complex128) {
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
	for size := 2; size <= n; size <<= 1 {
		step := frameLen / size
		for start := 0; start < n; start += size {
			for k := range size / 2 {
				u, v := a[start+k], a[start+k+size/2]*twiddle[k*step]
				a[start+k], a[start+k+size/2] = u+v, u-v
			}
		}
	}
}
