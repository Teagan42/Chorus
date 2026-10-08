package ui

// DoneCard confirms what happened and where it went, with Undo.
type DoneCard struct {
	Title   string
	Body    string
	Actions []Button
}
