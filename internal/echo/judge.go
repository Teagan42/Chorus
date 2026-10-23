package echo

import "math"

// Defaults, measured on the household's own voices through a simulated
// speaker, room and echo canceller (ADR-0096). Unmeasured on a real room: the
// gate journals every candidate's score, so the threshold can be.
const (
	// DefaultThreshold is the score at or above which mic audio is the
	// device's own voice. At 0.6 the simulated kitchen caught 125 of 127
	// echo windows and called 2 of 580 people speaking alone an echo -- both
	// saying the same words the device was saying.
	DefaultThreshold = 0.6

	// DefaultVoiced is the level a mic row must reach to be compared, RMS
	// as a fraction of full scale: -40 dBFS, listen.DefaultSpeechEnergy.
	DefaultVoiced = 0.01

	// DefaultWindow is the newest audio a candidate is judged on, in
	// frames: 800 ms. An utterance the endpointer opened on the echo and a
	// person then talked into is judged mostly on the person.
	DefaultWindow = 800 * rate / 1000

	// MinVoiced is the least voiced audio judged at all, in frames: 300 ms.
	// Shorter, and two unrelated voices line up by chance often enough to
	// call a person an echo.
	MinVoiced = 300 * rate / 1000

	// DefaultMaxLag is how long after the DAC's report the played audio may
	// reach the uplink, in frames: 600 ms of capture buffering, the device's
	// send batching, and the speaker's underrun silence (SPEC §3.3.2).
	DefaultMaxLag = 600 * rate / 1000

	// DefaultMaxLead is how far the mic may run ahead of the newest report,
	// in frames: one firmware loop, which publishes PLAYED before it pumps
	// the uplink, with margin.
	DefaultMaxLead = 100 * rate / 1000
)

// sounding is the reference level, RMS as a fraction of full scale, at which
// a row carries the device's voice: -50 dBFS.
const sounding = 0.003

// minSounding is the fraction of the window the reference must be voiced
// over, at a lag, for that lag to be compared. A window lined up against the
// last syllable of an answer correlates with whatever starts with a syllable.
const minSounding = 0.3

// Judge compares mic audio against what the device played. The zero value
// takes every default.
type Judge struct {
	Threshold float64
	// Voiced is the mic level a row must reach to be compared: the
	// satellite's own speech level (ADR-0083). The quiet before a person
	// starts is a step that any onset in the reference lines up with.
	Voiced  float64
	Window  int
	MaxLag  int
	MaxLead int
}

// Verdict is one comparison.
type Verdict struct {
	// Judged is false when there was nothing to compare: too little voiced
	// mic audio, or a reference silent at every lag. It is then no echo.
	Judged bool
	// Score is the best spectral-shape correlation found, from 0 to 1.
	Score float64
	// Lag is where it was found: frames from the DAC's report to the played
	// audio reaching the uplink. Negative is the mic ahead of the report.
	Lag int
	// Echo is a judged score at or over the threshold.
	Echo bool
}

func (j Judge) defaults() Judge {
	if j.Threshold <= 0 {
		j.Threshold = DefaultThreshold
	}
	if j.Voiced <= 0 {
		j.Voiced = DefaultVoiced
	}
	if j.Window <= 0 {
		j.Window = DefaultWindow
	}
	if j.MaxLag <= 0 {
		j.MaxLag = DefaultMaxLag
	}
	if j.MaxLead <= 0 {
		j.MaxLead = DefaultMaxLead
	}
	return j
}

// Recent judges the newest Window of mic, 16-bit little-endian PCM whose last
// frame arrived while the DAC reported position at. A nil reference judges
// nothing.
func (j Judge) Recent(ref *Reference, mic []byte, at uint64) Verdict {
	if ref == nil {
		return Verdict{}
	}
	j = j.defaults()
	x := samples(mic)
	if len(x) > j.Window {
		x = x[len(x)-j.Window:]
	}
	var voiced []int
	for i, lv := range levels(x) {
		if lv >= j.Voiced {
			voiced = append(voiced, i)
		}
	}
	if len(voiced)*hop < MinVoiced {
		return Verdict{}
	}
	// Every reference frame the window could hold: from the latest lag
	// before its first frame to the furthest lead past its last.
	from := int64(at) - int64(len(x)) - int64(j.MaxLag)
	r := ref.span(from, len(x)+j.MaxLag+j.MaxLead)
	v := compare(featurize(x), featurize(r), voiced, levels(r))
	if !v.Judged {
		return v
	}
	// Row k of the reference lines up with the mic's first row, so at k=0
	// the echo arrived MaxLag after the report.
	v.Lag = j.MaxLag - v.Lag*hop
	v.Echo = v.Score >= j.Threshold
	return v
}

// compare slides mic along ref and returns the best mean per-band Pearson
// correlation over the mic rows given, with the row offset it was found at
// in Lag. Offsets where the reference is mostly silent are skipped.
func compare(mic, ref spectrogram, rows []int, refLevels []float64) Verdict {
	t, n := len(mic), len(rows)
	if n < 2 || len(ref) < t {
		return Verdict{}
	}
	var mz [bands][]float64
	for b := range bands {
		mz[b] = zscore(column(mic, b, rows))
	}
	best := Verdict{}
	for k := 0; k+t <= len(ref); k++ {
		if voicedFraction(refLevels[k:k+t]) < minSounding {
			continue
		}
		var sum float64
		for b := range bands {
			sum += pearson(mz[b], ref, b, k, rows)
		}
		score := max(sum/bands, 0)
		if !best.Judged || score > best.Score {
			best = Verdict{Judged: true, Score: score, Lag: k}
		}
	}
	return best
}

// pearson correlates z, already z-scored, with band b of ref's rows offset
// by k. z sums to zero, so the reference's mean drops out of the product and
// only its spread is needed.
func pearson(z []float64, ref spectrogram, b, k int, rows []int) float64 {
	var sum, ss, prod float64
	for i, r := range rows {
		v := ref[k+r][b]
		sum += v
		ss += v * v
		prod += z[i] * v
	}
	n := float64(len(rows))
	variance := ss/n - (sum/n)*(sum/n)
	if variance < 1e-18 {
		return 0
	}
	return prod / n / math.Sqrt(variance)
}

// voicedFraction is the fraction of rows at or over the sounding level.
func voicedFraction(lv []float64) float64 {
	var n int
	for _, v := range lv {
		if v >= sounding {
			n++
		}
	}
	return float64(n) / float64(len(lv))
}

// column is band b of the given rows.
func column(s spectrogram, b int, rows []int) []float64 {
	c := make([]float64, len(rows))
	for i, r := range rows {
		c[i] = s[r][b]
	}
	return c
}

// zscore centres and scales c in place; a flat column becomes all zeros,
// which correlates with nothing.
func zscore(c []float64) []float64 {
	var mean float64
	for _, v := range c {
		mean += v
	}
	mean /= float64(len(c))
	var ss float64
	for i := range c {
		c[i] -= mean
		ss += c[i] * c[i]
	}
	sd := math.Sqrt(ss / float64(len(c)))
	if sd < 1e-9 {
		clear(c)
		return c
	}
	for i := range c {
		c[i] /= sd
	}
	return c
}
