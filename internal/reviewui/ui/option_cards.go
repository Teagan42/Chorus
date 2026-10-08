package ui

// OptionCards is a single choice between a few big options that each need a
// line of explanation, e.g. which dataset to export.
type OptionCards struct {
	ID    string
	Label string
	Items []OptionCard
}

type OptionCard struct {
	Label string
	Sub   string // mono line under the label
	On    bool
	Hx    Hx
}
