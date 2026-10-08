package ui

// Switch is the small link switch under a page eyebrow, e.g. Curate’s
// “DPO pairs | Hard negatives”.
type Switch struct {
	Label string
	Items []SwitchItem
}

type SwitchItem struct {
	Label string
	Href  string
	On    bool
}
