package ui

// ThumbKind is what a thumbnail segment shows.
type ThumbKind string

const (
	ThumbUser    ThumbKind = "user"    // upper band, neutral
	ThumbSpeech  ThumbKind = "speech"  // upper band, Voice
	ThumbUnheard ThumbKind = "unheard" // upper band, hatched Voice
	ThumbTool    ThumbKind = "tool"    // lower band, Home
	ThumbCut     ThumbKind = "cut"     // full height, People
)

type ThumbSeg struct {
	Kind        ThumbKind
	Left, Width float64 // 0–100
}

// ConvThumb is the shape of a conversation in a list row: a fixed window
// (12 s by default) so shapes compare across rows. Longer sessions set
// Overflow and get a ▸ at the right edge.
type ConvThumb struct {
	Window   float64
	Segs     []ThumbSeg
	Overflow bool
}

// NewConvThumb starts a thumbnail over window seconds (0 → 12).
func NewConvThumb(window float64) *ConvThumb {
	if window <= 0 {
		window = 12
	}
	return &ConvThumb{Window: window}
}

// Add places a segment from..to seconds. For ThumbCut pass the same time twice.
func (t *ConvThumb) Add(kind ThumbKind, from, to float64) *ConvThumb {
	if from > t.Window {
		t.Overflow = true
		return t
	}
	if to > t.Window {
		to, t.Overflow = t.Window, true
	}
	t.Segs = append(t.Segs, ThumbSeg{Kind: kind, Left: from / t.Window * 100, Width: (to - from) / t.Window * 100})
	return t
}
