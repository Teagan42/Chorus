package ui

import "fmt"

// Axis maps seconds onto a 0–100 position along a timeline. Every track,
// pin and overlay in one view must use the same Axis.
type Axis interface {
	Pos(t float64) float64
}

// Linear covers [From, To] seconds edge to edge.
type Linear struct{ From, To float64 }

func (a Linear) Pos(t float64) float64 {
	if a.To <= a.From {
		return 0
	}
	return clamp((t - a.From) / (a.To - a.From) * 100)
}

// Span is one visible stretch of a Gapped axis.
type Span struct {
	From, To float64 // seconds
	Start    float64 // where the span begins, 0–100
	Width    float64 // how much of the axis it takes, 0–100
}

// Gapped shows several time spans with the dead time between them collapsed,
// e.g. a conversation that left one room and resumed in another.
type Gapped struct{ Spans []Span }

func (a Gapped) Pos(t float64) float64 {
	for i, s := range a.Spans {
		if t <= s.To || i == len(a.Spans)-1 {
			if t < s.From {
				return s.Start
			}
			return clamp(s.Start + (t-s.From)/(s.To-s.From)*s.Width)
		}
	}
	return 0
}

// Gaps returns the collapsed regions between spans, for the "held" overlay.
func (a Gapped) Gaps() []Overlay {
	var out []Overlay
	for i := 1; i < len(a.Spans); i++ {
		prev, next := a.Spans[i-1], a.Spans[i]
		left := prev.Start + prev.Width
		out = append(out, Overlay{
			Kind:  OverlayGap,
			Pos:   left,
			Width: next.Start - left,
			Label: "+" + Clock(next.From-prev.To),
		})
	}
	return out
}

// Ticks returns ruler labels every step seconds across a Linear axis.
func (a Linear) Ticks(step float64, unit string) []Tick {
	var out []Tick
	for t := a.From; t <= a.To-step/2; t += step {
		label := fmt.Sprintf("%g", t)
		out = append(out, Tick{Pos: a.Pos(t), Label: label})
	}
	if len(out) > 0 && unit != "" {
		out[len(out)-1].Label += " " + unit
	}
	return out
}

// Clock formats seconds as m:ss.
func Clock(sec float64) string {
	s := int(sec + 0.5)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// Timecode formats seconds as mm:ss.mmm for the transport.
func Timecode(sec float64) string {
	ms := int(sec*1000 + 0.5)
	return fmt.Sprintf("%02d:%02d.%03d", ms/60000, (ms%60000)/1000, ms%1000)
}

func clamp(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
