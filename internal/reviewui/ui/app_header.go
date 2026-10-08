package ui

// StepID names one stage of the review workflow.
type StepID string

const (
	StepBrowse StepID = "browse"
	StepTriage StepID = "triage"
	StepReview StepID = "review"
	StepReplay StepID = "replay"
	StepCurate StepID = "curate"
	StepExport StepID = "export"
)

// Routes maps workflow steps to URLs. Override entries to match your router.
var Routes = map[StepID]string{
	StepBrowse: "/conversations",
	StepTriage: "/queue",
	StepReview: "/review",
	StepReplay: "/replays",
	StepCurate: "/curate/pairs",
	StepExport: "/export",
}

// Step is one numbered entry in the header.
type Step struct {
	ID      StepID
	N       string
	Label   string
	Href    string
	Count   int // shown as a badge when > 0 (Triage uses it for the queue size)
	Current bool
}

// AppHeader is the branded bar on every screen: icon, wordmark, the six
// workflow steps, and a small context string on the right.
type AppHeader struct {
	Home    string
	IconSrc string
	Steps   []Step
	Context string // e.g. "persona v14 · schema v6 · qwen3-32b"
}

// CurrentStep returns the underlined step (zero value if none).
func (h AppHeader) CurrentStep() Step {
	for _, s := range h.Steps {
		if s.Current {
			return s
		}
	}
	return Step{N: "", Label: "Menu"}
}

// NewAppHeader builds the standard header with current underlined.
func NewAppHeader(current StepID, queueCount int, context string) AppHeader {
	defs := []struct {
		id    StepID
		label string
	}{
		{StepBrowse, "Browse"},
		{StepTriage, "Triage"},
		{StepReview, "Review"},
		{StepReplay, "Replay"},
		{StepCurate, "Curate"},
		{StepExport, "Export"},
	}
	h := AppHeader{Home: Routes[StepBrowse], IconSrc: "/static/img/chorus-icon.png", Context: context}
	for i, d := range defs {
		s := Step{ID: d.id, N: "0" + string(rune('1'+i)), Label: d.label, Href: Routes[d.id], Current: d.id == current}
		if d.id == StepTriage {
			s.Count = queueCount
		}
		h.Steps = append(h.Steps, s)
	}
	return h
}
