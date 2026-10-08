package ui

// Inspector shows the selected journal event. Unheard is generated speech
// the user never heard; it renders struck through after Body.
type Inspector struct {
	Cap     string // "#0035 · 5.45 s · SPEAK · QUEUE · INTERRUPTED"
	Body    string
	Unheard string
	Meta    string
	Foot    string // e.g. "in effect: persona v14 · tool-schema v6"
	OOB     bool   // render with hx-swap-oob so a pin click can update it alongside the timeline
}
