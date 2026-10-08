package ui

// ClipTile is one stage-two wake rejection in the Hard negatives grid.
// Checks are the three stage-two tests: rescore, speech present, speaker match.
type ClipTile struct {
	ID         string
	Title      string // best guess: "Cough", "TV ad — “Hey, Eddie!”"
	Verdict    SigTag // "2 of 3 passed" (Voice) needs ears; "auto negative" (Muted) settled
	Peaks      []float64
	Checks     []Check
	Where      string // "Living room · 20:06"
	Reviewed   bool
	PlayHx     Hx
	NegHx      Hx // mark as negative
	WakeHx     Hx // mark as a real wake (stage two falsely rejected a person)
	MarkedNeg  bool
	MarkedWake bool
}

// ClipGrid lays tiles out with inset hairlines; Empty shows when no tiles.
type ClipGrid struct {
	ID    string
	Tiles []ClipTile
	Empty *EmptyState
}
