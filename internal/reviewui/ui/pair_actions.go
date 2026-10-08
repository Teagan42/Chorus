package ui

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
)

// PairStatus is where a DPO pair sits in Curate.
type PairStatus string

const (
	PairUnreviewed PairStatus = "unreviewed"
	PairEdited     PairStatus = "edited"
	PairAccepted   PairStatus = "accepted"
	PairDiscarded  PairStatus = "discarded"
)

// PairSource is how the pair was produced.
type PairSource string

const (
	SourceBargeIn    PairSource = "barge-in"
	SourceAnnotation PairSource = "annotation"
	SourceReplay     PairSource = "replay"
)

// Pair is the state the pair-actions component edits. Persist it however you
// like; the component only needs these fields.
//
// The rule it enforces: rejected and chosen must answer the same prompt. A
// barge-in harvests "what it said after the correction" as chosen — but that
// answers the correction, not the shared prompt. Accepting it unchanged trips
// the guard.
type Pair struct {
	ID              string
	Source          PairSource
	Rejected        string // what it was saying (heard part)
	RejectedUnheard string // generated, never heard
	Heard           string // the correction the user gave, e.g. “Just the first one.”
	AsSaid          string // what the assistant said after the correction
	Chosen          string // current chosen side; starts as AsSaid for barge-ins
	Suggestion      string // model rewrite of AsSaid against the shared prompt
	Status          PairStatus
	PrevStatus      PairStatus
	Unfixed         bool // accepted anyway while mismatched; export must skip it
	Reason          string
}

// Mismatch reports whether the chosen side still answers the correction.
func (p Pair) Mismatch() bool {
	return p.Source == SourceBargeIn && strings.TrimSpace(p.Chosen) == strings.TrimSpace(p.AsSaid)
}

// Exportable reports whether a DPO export may include this pair.
func (p Pair) Exportable() bool {
	return p.Status == PairAccepted && !p.Unfixed && !p.Mismatch()
}

// PairMode is which state of the component is showing.
type PairMode string

const (
	PairModeView    PairMode = "view"
	PairModeGuard   PairMode = "guard"
	PairModeEdit    PairMode = "edit"
	PairModeDiscard PairMode = "discard"
	PairModeDone    PairMode = "done"
)

// DiscardReasons are offered when discarding. "Barge-in was noise" should also
// file the clip with the barge-in gate corpus on your side.
var DiscardReasons = []string{"Barge-in was noise", "Not a preference", "Duplicate", "Other"}

// Checker produces the advisory checks under the editor.
type Checker func(p Pair, draft string) []Check

// DefaultChecker is a placeholder heuristic. Replace the first check with a
// real "answers the shared prompt" judgement before trusting it.
func DefaultChecker(p Pair, draft string) []Check {
	d := strings.TrimSpace(draft)
	words := len(strings.Fields(d))
	return []Check{
		{Text: "answers the shared prompt", On: d != "" && !(p.Source == SourceBargeIn && d == strings.TrimSpace(p.AsSaid))},
		{Text: "honours the correction", On: d != "" && d != strings.TrimSpace(p.Rejected+p.RejectedUnheard)},
		{Text: "short enough to hear", On: d != "" && words <= 25},
	}
}

// Apply runs one action against the pair and returns the mode to show next.
// form carries "chosen" (save, check) and "reason" (reason).
func (p *Pair) Apply(action string, form url.Values) (mode PairMode, draft string, err error) {
	switch action {
	case "accept":
		if p.Mismatch() {
			return PairModeGuard, "", nil
		}
		p.PrevStatus, p.Status = p.Status, PairAccepted
		return PairModeDone, "", nil
	case "accept-anyway":
		p.PrevStatus, p.Status, p.Unfixed = p.Status, PairAccepted, true
		return PairModeDone, "", nil
	case "edit":
		return PairModeEdit, p.Chosen, nil
	case "draft":
		return PairModeEdit, p.Suggestion, nil
	case "check":
		return PairModeEdit, form.Get("chosen"), nil
	case "save":
		if c := strings.TrimSpace(form.Get("chosen")); c != "" {
			p.Chosen = c
		}
		// A save that leaves the mismatch in place fixes nothing.
		if !p.Mismatch() {
			p.Unfixed = false
		}
		if p.Status == PairUnreviewed {
			p.PrevStatus, p.Status = p.Status, PairEdited
		}
		return PairModeView, "", nil
	case "cancel":
		return PairModeView, "", nil
	case "discard":
		return PairModeDiscard, "", nil
	case "reason":
		p.PrevStatus, p.Status, p.Reason = p.Status, PairDiscarded, form.Get("reason")
		return PairModeDone, "", nil
	case "undo":
		if p.PrevStatus != "" {
			p.Status = p.PrevStatus
		}
		p.Unfixed, p.Reason = false, ""
		return PairModeView, "", nil
	}
	return PairModeView, "", fmt.Errorf("pair: unknown action %q", action)
}

// PairReason is a discard reason button.
type PairReason struct {
	Label string
	Vals  string // hx-vals JSON
}

// PairActions is the view model for the "pair-actions" partial.
type PairActions struct {
	ID              string
	Endpoint        string // actions POST to Endpoint + "/" + action
	Cap             string // "DPO PAIR · HARVESTED FROM BARGE-IN #0041"
	Status          SigTag
	Mode            PairMode
	Rejected        string
	RejectedUnheard string
	Heard           string
	Chosen          string
	Provenance      string
	ProvTone        Tone
	Draft           string
	Checks          []Check
	CanDraft        bool
	Reasons         []PairReason
	Guard           Alert
	Done            DoneCard
}

// NewPairActions builds the view model for pair p in mode.
func NewPairActions(p Pair, mode PairMode, draft, endpoint, cap string, check Checker) PairActions {
	if check == nil {
		check = DefaultChecker
	}
	post := func(action string) Hx { return Hx{Post: endpoint + "/" + action} }
	pa := PairActions{
		ID: p.ID, Endpoint: endpoint, Cap: cap, Mode: mode,
		Rejected: p.Rejected, RejectedUnheard: p.RejectedUnheard, Heard: p.Heard,
		Chosen: p.Chosen, Draft: draft, CanDraft: p.Suggestion != "",
	}
	switch {
	case p.Unfixed:
		pa.Status = SigTag{Text: string(p.Status) + " · mismatch", Tone: ToneConv}
	case p.Status == PairEdited:
		pa.Status = SigTag{Text: string(p.Status), Tone: ToneConv}
	case p.Status == PairAccepted:
		pa.Status = SigTag{Text: string(p.Status), Tone: ToneHuman}
	default:
		pa.Status = SigTag{Text: string(p.Status), Tone: ToneMuted}
	}
	switch {
	case p.Mismatch():
		pa.Provenance, pa.ProvTone = "as said · answers "+p.Heard+", not the shared prompt", ToneConv
	case p.Source == SourceAnnotation:
		pa.Provenance = "authored by the reviewer"
	case p.Source == SourceReplay:
		pa.Provenance = "replay output · same prompt"
	case p.Chosen == p.Suggestion:
		pa.Provenance = "drafted from the correction · reviewed · answers the shared prompt"
	default:
		pa.Provenance = "edited by you · answers the shared prompt"
	}
	if mode == PairModeEdit {
		pa.Checks = check(p, draft)
	}
	for _, r := range DiscardReasons {
		b, _ := json.Marshal(map[string]string{"reason": r})
		pa.Reasons = append(pa.Reasons, PairReason{Label: r, Vals: string(b)})
	}
	pa.Guard = Alert{
		Title: "This chosen answer replies to the wrong prompt.",
		Body:  "It answers " + p.Heard + ", but the rejected side answers the shared prompt. Trained as-is, the pair teaches the model to drop half the question.",
		Note:  "accepting anyway keeps it in Accepted, flagged; export skips it until it’s fixed",
		Tone:  ToneConv,
		Actions: []Button{
			{Label: "Fix it first", Primary: true, Hx: post("edit")},
			{Label: "Accept anyway", Hx: post("accept-anyway")},
			{Label: "Cancel", Hx: post("cancel")},
		},
	}
	if mode == PairModeDone {
		switch {
		case p.Status == PairDiscarded:
			pa.Done = DoneCard{Title: "Discarded · " + p.Reason, Body: "Out of the export pool. The trace and audio stay in the journal."}
		case p.Unfixed:
			pa.Done = DoneCard{Title: "Accepted as-is · flagged", Body: "It sits in Accepted marked “mismatch”. Export skips it until the chosen side answers the shared prompt."}
			pa.Done.Actions = append(pa.Done.Actions, Button{Label: "Fix chosen", Primary: true, Hx: post("edit")})
		default:
			pa.Done = DoneCard{Title: "Accepted · " + p.ID, Body: "Both sides answer the same prompt. It goes into the next DPO export."}
		}
		pa.Done.Actions = append(pa.Done.Actions, Button{Label: "Undo", Hx: post("undo")})
	}
	return pa
}

// WithDoneLinks adds "Open in Curate" and/or "Next unreviewed" to the done card.
func (pa PairActions) WithDoneLinks(curateHref string, next *Hx) PairActions {
	if pa.Mode != PairModeDone {
		return pa
	}
	if curateHref != "" {
		pa.Done.Actions = append(pa.Done.Actions, Button{Label: "Open in Curate", Href: curateHref})
	}
	if next != nil {
		pa.Done.Actions = append(pa.Done.Actions, Button{Label: "Next unreviewed", Hx: *next})
	}
	return pa
}

// ServePairAction is a ready-made htmx handler body: apply action to p and
// render the swapped component. Persist p after it returns without error.
// "check" renders only the checks list so typing doesn’t replace the textarea.
func ServePairAction(w http.ResponseWriter, r *http.Request, tpl *template.Template, p *Pair, action, endpoint, cap string, check Checker) error {
	if err := r.ParseForm(); err != nil {
		return err
	}
	mode, draft, err := p.Apply(action, r.PostForm)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return err
	}
	pa := NewPairActions(*p, mode, draft, endpoint, cap, check)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if action == "check" {
		return tpl.ExecuteTemplate(w, "pair-checks", pa)
	}
	return tpl.ExecuteTemplate(w, "pair-actions", pa)
}
