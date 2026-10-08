package ui

// Icon names the inline SVG glyphs the kit ships: "play", "pause", "prev", "next".
type Icon string

const (
	IconNone  Icon = ""
	IconPlay  Icon = "play"
	IconPause Icon = "pause"
	IconPrev  Icon = "prev"
	IconNext  Icon = "next"
)

// Button renders as <a> when Href is set (and not Disabled), otherwise <button>.
// Primary is the inverted surface style: one per region. Buttons are never
// coloured — colour is reserved for meaning.
type Button struct {
	ID        string
	Label     string
	Href      string
	Type      string // button (default) | submit
	Name      string // form field name for submit buttons
	Value     string
	AriaLabel string // required when the button is icon-only
	Title     string
	Icon      Icon
	Primary   bool
	Hit       bool // 44px square/target: transport and touch-critical actions
	Disabled  bool
	Toggle    bool // emit aria-pressed
	Pressed   bool
	Hx        Hx
}
