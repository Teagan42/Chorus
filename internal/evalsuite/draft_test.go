package evalsuite_test

import (
	"context"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/evalsuite"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// The reviewer's evening: the shopping list was exactly right, the oven
// timer should have asked how long, and Teagan's cut forecast was settled
// on her shorter answer.
func judged(t *testing.T) *curation.MemStore {
	t.Helper()
	ctx := context.Background()
	at := household.ReviewedAt()
	st := curation.NewMemStore()
	for _, a := range []curation.Annotation{
		{ConversationID: household.ConvList, Seq: 2, Labels: []curation.Label{curation.LabelExemplar}, AnnotatedAt: at},
		{ConversationID: household.ConvTimer, Seq: 2, Labels: []curation.Label{curation.LabelMisunderstoodIntent, curation.LabelTooSlow}, ShouldHave: "Ask how long the oven timer should be.", AnnotatedAt: at},
		// A label with no correction says what went wrong, not what is right.
		{ConversationID: household.ConvGarage, Seq: 2, Labels: []curation.Label{curation.LabelTooSlow}, AnnotatedAt: at},
	} {
		if err := st.PutAnnotation(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Put(ctx, curation.Decision{
		PairID: household.PairWeather, ConversationID: household.ConvWeather, Status: curation.StatusAccepted,
		Chosen: "Rain from three, high of fourteen.", DecidedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	// Discarded: no case.
	if err := st.Put(ctx, curation.Decision{
		PairID: household.PairZeppelin, ConversationID: household.ConvZeppelin, Status: curation.StatusDiscarded,
		Reason: "not a correction", DecidedAt: at,
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

// A suite can start from what reviewers already judged: each exemplar
// expects the calls it made, each correction and each accepted barge-in is a
// reference answer. The draft parses and resolves as written, for a person
// to tighten and check in.
//
// verifies SPEC §9.2, §13
func TestADraftSuiteStartsFromTheReviewersJudgements(t *testing.T) {
	store := day(t)
	draft, err := evalsuite.Draft(context.Background(), store, judged(t), "thursday-judged")
	if err != nil {
		t.Fatal(err)
	}
	b, err := draft.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	s, err := evalsuite.Parse(b)
	if err != nil {
		t.Fatalf("the draft does not parse: %v\n%s", err, b)
	}
	if _, err := evalsuite.Resolve(context.Background(), store, daysContexts(), s); err != nil {
		t.Fatalf("the draft does not resolve: %v\n%s", err, b)
	}
	byRef := map[string]evalsuite.Case{}
	for _, c := range s.Cases {
		byRef[c.Ref] = c
	}
	if len(s.Cases) != 3 {
		t.Errorf("%d cases, want the exemplar, the correction and the accepted pair:\n%s", len(s.Cases), b)
	}
	list := byRef[household.ConvList+"/2"]
	if list.From != "exemplar" || len(list.Expect.ToolCalled) != 1 || list.Expect.ToolCalled[0].Args["entity_id"] != "todo.shopping_list" || !list.Expect.NoInventedEntities {
		t.Errorf("exemplar = %+v", list)
	}
	timer := byRef[household.ConvTimer+"/2"]
	if timer.From != "annotation" || timer.Expect.Reference != "Ask how long the oven timer should be." || timer.Expect.FirstSpeechMS == 0 {
		t.Errorf("correction = %+v, want its note as the reference and too_slow as a budget", timer)
	}
	weather := byRef[household.ConvWeather+"/2"]
	if weather.From != "barge-in" || weather.Expect.Reference != "Rain from three, high of fourteen." || weather.Expect.MinReferenceOverlap == 0 {
		t.Errorf("accepted pair = %+v", weather)
	}
	if !strings.HasPrefix(string(b), "name: thursday-judged\n") {
		t.Errorf("draft opens:\n%s", b)
	}
}
