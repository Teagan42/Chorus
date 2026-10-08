package ui

// Check is one advisory test shown as a filled (passed) or hollow dot.
// Shape, not hue, carries pass/fail. Checks advise; they never block.
type Check struct {
	Text string
	On   bool
}
