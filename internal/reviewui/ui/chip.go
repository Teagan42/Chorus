package ui

// Chip is a toggle label, e.g. the annotation vocabulary.
type Chip struct {
	Label string
	On    bool
	Hx    Hx
}

// ChipGroup lays out chips with an optional caps label.
type ChipGroup struct {
	Label string
	Chips []Chip
}
