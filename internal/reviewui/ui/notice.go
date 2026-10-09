package ui

// Notice is a strip above the app header saying what kind of instance this
// is, such as a demo whose verdicts vanish on reload. A real deployment has
// none.
type Notice struct {
	Label    string // a word or two, set in caps
	Text     string
	LinkText string
	Href     string
}
