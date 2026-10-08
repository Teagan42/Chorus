package ui

// LegendItem pairs a swatch with a label. Shape: "block", "outline",
// "flag" (block with a coloured top edge), "tick", "hatch", "presence".
type LegendItem struct {
	Label string
	Shape string
	Tone  Tone // swatch colour; for "flag" this is the flag colour on a Conversation block
}

type Legend struct {
	Items []LegendItem
}
