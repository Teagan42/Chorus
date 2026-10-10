package curation

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Label is one word of SPEC §9.2's annotation vocabulary: what a reviewer
// says went wrong with a turn, or that it is worth learning from.
type Label string

const (
	LabelTranscriptWrong     Label = "transcript_wrong"
	LabelMisunderstoodIntent Label = "misunderstood_intent"
	LabelWrongTool           Label = "wrong_tool"
	LabelShouldHaveSpoken    Label = "should_have_spoken"
	LabelSpokeWhenShouldnt   Label = "spoke_when_it_shouldnt"
	LabelTooSlow             Label = "too_slow"
	LabelWrongPerson         Label = "wrong_person"
	LabelExemplar            Label = "exemplar"
)

// Labels is the vocabulary in SPEC order, which is the order a turn's labels
// are stored and shown in.
var Labels = []Label{
	LabelTranscriptWrong, LabelMisunderstoodIntent, LabelWrongTool, LabelShouldHaveSpoken,
	LabelSpokeWhenShouldnt, LabelTooSlow, LabelWrongPerson, LabelExemplar,
}

// Fault reports whether the label says the turn went wrong. Only "good —
// exemplar" does not.
func (l Label) Fault() bool { return l != LabelExemplar }

// Annotation is a reviewer's labels on one turn, and what it should have done
// in their words. A turn is its utterance's seq, as Replay and the harvester
// name it. The last write wins, as with a Decision.
type Annotation struct {
	ConversationID string
	Seq            uint64
	Labels         []Label
	ShouldHave     string
	AnnotatedAt    time.Time
}

// Empty is an annotation that says nothing; storing one deletes it.
func (a Annotation) Empty() bool {
	return len(a.Labels) == 0 && strings.TrimSpace(a.ShouldHave) == ""
}

// Has reports whether the turn carries the label.
func (a Annotation) Has(l Label) bool { return slices.Contains(a.Labels, l) }

// Toggle adds the label, or takes it off if the turn already carries it,
// keeping the vocabulary's order.
func (a Annotation) Toggle(l Label) Annotation {
	on := map[Label]bool{l: !a.Has(l)}
	for _, x := range a.Labels {
		if x != l {
			on[x] = true
		}
	}
	a.Labels = nil
	for _, x := range Labels {
		if on[x] {
			a.Labels = append(a.Labels, x)
		}
	}
	return a
}

// Faulted is an annotation that says the turn went wrong and what it should
// have done instead: the two halves of a preference pair (SPEC §9.2).
func (a Annotation) Faulted() bool {
	if strings.TrimSpace(a.ShouldHave) == "" {
		return false
	}
	return slices.ContainsFunc(a.Labels, Label.Fault)
}

func (a Annotation) validate() error {
	if a.ConversationID == "" {
		return fmt.Errorf("curation: annotation of turn %d without a conversation", a.Seq)
	}
	if a.Seq == 0 {
		return fmt.Errorf("curation: annotation in %s without a turn", a.ConversationID)
	}
	for _, l := range a.Labels {
		if !slices.Contains(Labels, l) {
			return fmt.Errorf("curation: annotation %s/%d with label %q", a.ConversationID, a.Seq, l)
		}
	}
	return nil
}
