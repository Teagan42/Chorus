package ui

import (
	"fmt"
	"math"
	"strings"
)

// WavePath turns per-window peak levels (0–1, evenly spaced across the view)
// into a symmetric SVG path in a 1000×40 viewBox, for Track.Wave.
// Compute peaks server-side from the journal's PCM; ~200–400 points is plenty.
func WavePath(peaks []float64) string {
	n := len(peaks)
	if n < 2 {
		return ""
	}
	var b strings.Builder
	x := func(i int) float64 { return float64(i) / float64(n-1) * 1000 }
	amp := func(v float64) float64 { return math.Max(0.6, math.Min(1, v)*19) }
	fmt.Fprintf(&b, "M0 20")
	for i, v := range peaks {
		fmt.Fprintf(&b, " L%.1f %.1f", x(i), 20-amp(v))
	}
	for i := n - 1; i >= 0; i-- {
		fmt.Fprintf(&b, " L%.1f %.1f", x(i), 20+amp(peaks[i]))
	}
	b.WriteString(" Z")
	return b.String()
}

// BarsOver places one bar per peak across [from, to] seconds on an axis.
// Use this when a track shows audio for only part of the view (room change).
func BarsOver(axis Axis, from, to float64, peaks []float64) []Bar {
	if len(peaks) == 0 {
		return nil
	}
	step := (to - from) / float64(len(peaks))
	out := make([]Bar, len(peaks))
	for i, p := range peaks {
		out[i] = Bar{Pos: axis.Pos(from + float64(i)*step), Height: p}
	}
	return out
}
