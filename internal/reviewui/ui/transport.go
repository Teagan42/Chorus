package ui

// Transport is the play / timecode / channel row on trace screens.
type Transport struct {
	ID       string // set to make the transport an htmx swap target
	OOB      bool   // render with hx-swap-oob (needs ID)
	Playing  bool
	PlayHx   Hx
	Timecode string     // use Timecode(seconds)
	Channels *Segmented // e.g. AEC’d / Raw / TTS out / Mix, or Skip gap / Real time
	Actions  []Button   // prev / next / Edit & replay
}
