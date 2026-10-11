package ui

// Confirm is a destructive action behind a second, explicit press. The
// summary only opens a panel that says what goes and why it cannot come
// back; the button inside it is what posts. It needs no script, so the
// page keeps its policy, and without htmx the form still posts.
type Confirm struct {
	ID    string // the details element's id
	Label string // the first press: "Delete this conversation…"
	Title string // the question: "Delete conv-1930-living_room for good?"
	Body  string // what goes, and why it cannot be undone

	// Action is where the form posts; Confirm the value of its hidden
	// confirm field, which the server checks before it deletes anything.
	Action  string
	Confirm string
	Button  string // the second press: "Delete for good"

	// Target and Swap are htmx's; empty leaves the response to redirect.
	Target string
	Swap   string
	Tone   Tone
}
