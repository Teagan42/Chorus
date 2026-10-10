package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
	"github.com/teagan42/chorus/internal/triage"
)

// labelNames is SPEC §9.2's vocabulary as a reviewer reads it.
var labelNames = map[curation.Label]string{
	curation.LabelTranscriptWrong:     "Transcript wrong",
	curation.LabelMisunderstoodIntent: "Misunderstood intent",
	curation.LabelWrongTool:           "Wrong tool / args",
	curation.LabelShouldHaveSpoken:    "Should have spoken",
	curation.LabelSpokeWhenShouldnt:   "Spoke when it shouldn't",
	curation.LabelTooSlow:             "Too slow",
	curation.LabelWrongPerson:         "Wrong person",
	curation.LabelExemplar:            "Good — exemplar",
}

// turnAnnotation is the labels and note under one turn's utterance on the
// Conversation page, swapped whole on every change.
type turnAnnotation struct {
	ID     string
	Seq    uint64
	Labels ui.ChipGroup
	Note   ui.Field
	Save   ui.Button
	Action string

	// Pair links the preference pair the annotation makes in Curate, when it
	// says what went wrong and what should have happened instead.
	Pair *ui.Button

	// Again says the person asked this again: evidence the answer failed.
	Again string
}

func turnPath(conv string, seq uint64) string {
	return conversationHref(conv) + "/turns/" + strconv.FormatUint(seq, 10)
}

func annotationPairID(conv string, seq uint64) string {
	return fmt.Sprintf("%s/%d/%s", conv, seq, harvest.SourceAnnotation)
}

// againNote is what a turn's labels say of the repeat that followed it.
func againNote(r triage.Signal) string {
	return fmt.Sprintf("asked again at #%d: “%s”. A fault and what it should have said make a pair with the repeat as its evidence.", r.Seq, r.Utterance)
}

// askedAgain is the conversation's repeats, keyed by the turn each repeated.
func (s *server) askedAgain(ctx context.Context, conv string) (map[uint64]triage.Signal, error) {
	sigs, err := triage.Scan(ctx, s.journal, conv)
	if err != nil {
		return nil, err
	}
	out := map[uint64]triage.Signal{}
	for _, sig := range sigs {
		if sig.Kind == triage.KindRepeated {
			out[sig.First] = sig
		}
	}
	return out, nil
}

func annotationView(conv string, seq uint64, a curation.Annotation) turnAnnotation {
	id := fmt.Sprintf("turn-%d-labels", seq)
	note := fmt.Sprintf("turn-%d-should-have", seq)
	v := turnAnnotation{
		ID: id, Seq: seq, Action: turnPath(conv, seq) + "/note",
		Labels: ui.ChipGroup{Label: fmt.Sprintf("Turn #%d", seq)},
		Note: ui.Field{
			Kind: ui.FieldTextarea, ID: note, Name: "should_have", Rows: 2,
			Label: "What it should have done", Value: a.ShouldHave,
			Placeholder: "Say it as the reply you wanted; with a label, Curate offers it as the chosen side.",
		},
		Save: ui.Button{Label: "Save", Type: "submit"},
	}
	for _, l := range curation.Labels {
		v.Labels.Chips = append(v.Labels.Chips, ui.Chip{
			Label: labelNames[l], On: a.Has(l),
			// The note rides along, so a label never swaps away an unsaved draft.
			Hx: ui.Hx{
				Post: turnPath(conv, seq) + "/labels/" + string(l), Target: "#" + id, Swap: "outerHTML",
				Include: "#" + note,
			},
		})
	}
	if a.Faulted() {
		v.Pair = &ui.Button{Label: "In Curate ›", Href: pairHref(annotationPairID(conv, seq), "all")}
	}
	return v
}

// labelled is how a pair says which labels made it.
func labelled(a curation.Annotation) string {
	var names []string
	for _, l := range a.Labels {
		names = append(names, "“"+strings.ToLower(labelNames[l])+"”")
	}
	return "labelled " + strings.Join(names, ", ")
}

// turnOf finds the utterance that opens turn seq, or reports false when seq
// names no turn of the conversation.
func (s *server) turnOf(ctx context.Context, conv string, seq uint64) (bool, error) {
	events, err := s.journal.Events(ctx, conv)
	if err != nil {
		return false, err
	}
	for _, e := range events {
		if e.Seq == seq {
			return e.Kind == journal.KindUtteranceTranscribed, nil
		}
	}
	return false, nil
}

// annotate handles POST /conversations/{id}/turns/{seq}/labels/{label} and
// /conversations/{id}/turns/{seq}/note, and swaps the turn's annotation back.
func (s *server) annotate(w http.ResponseWriter, r *http.Request) {
	conv := r.PathValue("id")
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	ok, err := s.turnOf(ctx, conv, seq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	all, err := s.decisions.Annotations(ctx, conv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	before, ok := all[seq]
	if !ok {
		before = curation.Annotation{ConversationID: conv, Seq: seq}
	}
	after := before
	if l := curation.Label(r.PathValue("label")); l != "" {
		if _, known := labelNames[l]; !known {
			http.NotFound(w, r)
			return
		}
		after = before.Toggle(l)
	}
	again, err := s.askedAgain(ctx, conv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if _, drafted := r.PostForm["should_have"]; drafted {
		after.ShouldHave = strings.TrimSpace(r.PostForm.Get("should_have"))
	}
	after.AnnotatedAt = s.now()
	if err := s.decisions.PutAnnotation(ctx, after); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// A verdict judged the pair the annotation made; a new chosen side, no
	// pair at all, or calls taken off or put back (ADR-0054) needs a new one.
	if before.Faulted() != after.Faulted() || before.ShouldHave != after.ShouldHave ||
		before.Has(curation.LabelWrongTool) != after.Has(curation.LabelWrongTool) {
		if err := s.decisions.Delete(ctx, annotationPairID(conv, seq)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	v := annotationView(conv, seq, after)
	if r, ok := again[seq]; ok {
		v.Again = againNote(r)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.render(w, "turn-annotation", v)
}
