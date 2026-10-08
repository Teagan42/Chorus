package ui

// DiffLine is one line of a prompt or schema diff: Kind " ", "+" or "-".
type DiffLine struct {
	Kind string
	Text string
}

// CodeDiff renders a small mono diff, e.g. persona v14 → v15-draft.
type CodeDiff struct {
	Label string
	Lines []DiffLine
}
