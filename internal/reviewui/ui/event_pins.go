package ui

import "sort"

// Pin is a journal event placed at its moment on the Events track.
// Clicking it should load the inspector (Hx) and move the playhead.
type Pin struct {
	ID       string
	Label    string
	Pos      float64 // 0–100 on the shared axis
	Row      int     // 0-based; see AssignRows
	Tone     Tone    // people=user turn/barge, voice=speak, home=tool, conv=wake/end, muted=system
	Selected bool
	Hx       Hx
}

// PinRowGap is the vertical distance between pin rows in px.
const PinRowGap = 28

// StemHeight is the pin's total height from the lane top, label included.
func (p Pin) StemHeight() int { return p.Row*PinRowGap + 38 }

// AssignRows stacks pins greedily left to right so labels never collide,
// given the axis width in px and an approximate label width per character.
// Pins are returned in their original order.
func AssignRows(pins []Pin, axisPx float64) []Pin {
	const charPx, padPx, gapPx = 7.0, 16.0, 8.0
	idx := make([]int, len(pins))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return pins[idx[a]].Pos < pins[idx[b]].Pos })
	var rowEnd []float64
	out := append([]Pin(nil), pins...)
	for _, i := range idx {
		left := pins[i].Pos / 100 * axisPx
		width := float64(len([]rune(pins[i].Label)))*charPx + padPx
		row := -1
		for r, end := range rowEnd {
			if left >= end+gapPx {
				row = r
				break
			}
		}
		if row == -1 {
			row = len(rowEnd)
			rowEnd = append(rowEnd, 0)
		}
		rowEnd[row] = left + width
		out[i].Row = row
	}
	return out
}
