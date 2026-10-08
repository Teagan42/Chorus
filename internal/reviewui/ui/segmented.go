package ui

// Segmented is a small exclusive choice, e.g. audio channel AEC’d / Raw / TTS / Mix.
type Segmented struct {
	ID    string // set to make the group an htmx swap target
	Label string // aria-label for the group
	Items []SegItem
}

type SegItem struct {
	Label string
	On    bool
	Hx    Hx
}
