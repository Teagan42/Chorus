package ui

// Alert interrupts an action that would do something the reviewer probably
// doesn’t want. Say what is wrong and what it would cause; make the safe
// action primary; never hide the override.
type Alert struct {
	Title   string
	Body    string
	Note    string // small mono line under the actions
	Tone    Tone   // ToneConv for prompt/session problems
	Actions []Button
}
