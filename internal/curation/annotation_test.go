package curation_test

import (
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/curation"
)

// Labels land in the vocabulary's order whichever the reviewer clicks first,
// and a second click takes one off.
//
// verifies SPEC §9.2
func TestToggleKeepsTheVocabularysOrder(t *testing.T) {
	var a curation.Annotation
	a = a.Toggle(curation.LabelTooSlow).Toggle(curation.LabelTranscriptWrong).Toggle(curation.LabelWrongPerson)
	want := []curation.Label{curation.LabelTranscriptWrong, curation.LabelTooSlow, curation.LabelWrongPerson}
	if !reflect.DeepEqual(a.Labels, want) {
		t.Errorf("labels = %v, want %v", a.Labels, want)
	}
	a = a.Toggle(curation.LabelTooSlow)
	if a.Has(curation.LabelTooSlow) || len(a.Labels) != 2 {
		t.Errorf("after a second click labels = %v, want too_slow off", a.Labels)
	}
}

// A turn becomes a preference pair only when the reviewer says it went wrong
// and what it should have done. An exemplar is a positive, not a pair.
//
// verifies SPEC §9.2
func TestOnlyAFaultWithANoteMakesAPair(t *testing.T) {
	for _, c := range []struct {
		name   string
		labels []curation.Label
		note   string
		want   bool
	}{
		{"wrong tool, with the call it should have made", []curation.Label{curation.LabelWrongTool}, "Turn off the office light, not office_2.", true},
		{"wrong tool, nothing said about what instead", []curation.Label{curation.LabelWrongTool}, "  ", false},
		{"an exemplar with a note", []curation.Label{curation.LabelExemplar}, "Exactly this.", false},
		{"a note with no label", nil, "Asked which album first.", false},
		{"an exemplar that was also too slow", []curation.Label{curation.LabelTooSlow, curation.LabelExemplar}, "Same words, sooner.", true},
	} {
		a := curation.Annotation{ConversationID: "conv-0853-kitchen", Seq: 2, Labels: c.labels, ShouldHave: c.note}
		if got := a.Faulted(); got != c.want {
			t.Errorf("%s: faulted = %v, want %v", c.name, got, c.want)
		}
	}
}
