package ui

// Tabs filter a list in place. Count renders as a mono badge when ShowCount.
type Tabs struct {
	ID    string // set to make the tab bar an htmx swap target
	OOB   bool   // render with hx-swap-oob, e.g. to refresh counts after an action
	Label string
	Items []TabItem
}

type TabItem struct {
	Label     string
	Count     int
	ShowCount bool
	On        bool
	Href      string // use Href for page links, Hx for in-place swaps
	Hx        Hx
}
