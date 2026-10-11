package curation_test

import (
	"context"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

var judged = time.Date(2025, 10, 9, 22, 30, 0, 0, time.UTC)

// forgetConformance holds both stores to what retention needs of them
// (ADR-0065): what counts as curated, and forgetting a deleted log's rows.
var forgetConformance = map[string]func(*testing.T, curation.Store){
	// verifies SPEC §9.2
	"accepted, edited, annotated and promoted turns are curated; a discard is not": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		put := func(err error) {
			t.Helper()
			if err != nil {
				t.Fatal(err)
			}
		}
		put(s.Put(ctx, curation.Decision{PairID: "conv-kitchen-accepted/11", ConversationID: "conv-kitchen-accepted", Status: curation.StatusAccepted, Chosen: "Playing Led Zeppelin one.", DecidedAt: judged}))
		put(s.Put(ctx, curation.Decision{PairID: "conv-kitchen-edited/11", ConversationID: "conv-kitchen-edited", Status: curation.StatusEdited, Chosen: "Which album?", DecidedAt: judged}))
		put(s.Put(ctx, curation.Decision{PairID: "conv-kitchen-discarded/11", ConversationID: "conv-kitchen-discarded", Status: curation.StatusDiscarded, Reason: "noise", DecidedAt: judged}))
		put(s.PutAnnotation(ctx, curation.Annotation{ConversationID: "conv-office-labelled", Seq: 2, Labels: []curation.Label{curation.LabelExemplar}, AnnotatedAt: judged}))
		put(s.PutPromotion(ctx, curation.Promotion{ConversationID: "conv-garage-promoted", Seq: 2, Speech: "The garage is closed.", Versions: journal.Versions{Model: "m", Prompt: "p", ToolSchema: "t"}, PromotedAt: judged}))
		if _, err := s.AddRerun(ctx, curation.Rerun{ConversationID: "conv-garage-explored", Versions: journal.Versions{Model: "m", Prompt: "p", ToolSchema: "t"}, Takes: []curation.Take{{Seq: 2, Speech: "Maybe."}}, RanAt: judged}); err != nil {
			t.Fatal(err)
		}

		for conv, want := range map[string]bool{
			"conv-kitchen-accepted": true, "conv-kitchen-edited": true,
			"conv-office-labelled": true, "conv-garage-promoted": true,
			"conv-kitchen-discarded": false, "conv-garage-explored": false, "conv-untouched": false,
		} {
			got, err := curation.Curated(ctx, s, conv)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("curated(%s) = %v, want %v", conv, got, want)
			}
		}
	},

	// verifies SPEC §9.3
	"only a confirmed wake is curated": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		for seq, status := range map[uint64]curation.WakeStatus{2: curation.WakeConfirmed, 3: curation.WakeDiscarded} {
			if err := s.PutWakeVerdict(ctx, curation.WakeVerdict{ConversationID: "device:kitchen", Seq: seq, Status: status, JudgedAt: judged}); err != nil {
				t.Fatal(err)
			}
		}
		got, err := curation.ConfirmedWakes(ctx, s, "device:kitchen")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !got[2] {
			t.Errorf("confirmed = %v, want only #2", got)
		}
	},

	// verifies SPEC §9.2
	"forgetting a conversation removes every row of it and nothing else": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		f := forgetter(t, s)
		for _, conv := range []string{"conv-old", "conv-kept"} {
			if err := s.Put(ctx, curation.Decision{PairID: conv + "/11", ConversationID: conv, Status: curation.StatusDiscarded, Reason: "noise", DecidedAt: judged}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutAnnotation(ctx, curation.Annotation{ConversationID: conv, Seq: 2, Labels: []curation.Label{curation.LabelTooSlow}, AnnotatedAt: judged}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutPromotion(ctx, curation.Promotion{ConversationID: conv, Seq: 2, Speech: "Done.", Versions: journal.Versions{Model: "m", Prompt: "p", ToolSchema: "t"}, PromotedAt: judged}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.AddRerun(ctx, curation.Rerun{ConversationID: conv, Versions: journal.Versions{Model: "m", Prompt: "p", ToolSchema: "t"}, Takes: []curation.Take{{Seq: 2, Speech: "Done."}}, RanAt: judged}); err != nil {
				t.Fatal(err)
			}
		}
		if err := f.Forget(ctx, "conv-old"); err != nil {
			t.Fatalf("forget: %v", err)
		}
		if n := rows(t, s, "conv-old"); n != 0 {
			t.Errorf("conv-old keeps %d rows, want none", n)
		}
		if n := rows(t, s, "conv-kept"); n != 4 {
			t.Errorf("conv-kept keeps %d rows, want its 4", n)
		}
	},

	// verifies SPEC §9.3
	"forgetting wakes removes only the named verdicts": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		for _, seq := range []uint64{2, 3, 4} {
			if err := s.PutWakeVerdict(ctx, curation.WakeVerdict{ConversationID: "device:kitchen", Seq: seq, Status: curation.WakeDiscarded, JudgedAt: judged}); err != nil {
				t.Fatal(err)
			}
		}
		if err := forgetter(t, s).ForgetWakes(ctx, "device:kitchen", []uint64{2, 4}); err != nil {
			t.Fatalf("forget wakes: %v", err)
		}
		got, err := s.WakeVerdicts(ctx, "device:kitchen")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := got[3]; len(got) != 1 || !ok {
			t.Errorf("verdicts = %v, want only #3", got)
		}
	},
}

func forgetter(t *testing.T, s curation.Store) curation.Forgetter {
	t.Helper()
	f, ok := s.(curation.Forgetter)
	if !ok {
		t.Fatalf("%T cannot forget", s)
	}
	return f
}

// rows counts every row a conversation has, across the tables.
func rows(t *testing.T, s curation.Store, conv string) int {
	t.Helper()
	ctx := context.Background()
	ds, err := s.ForConversation(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	as, err := s.Annotations(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	ps, err := s.Promotions(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := s.Reruns(ctx, conv)
	if err != nil {
		t.Fatal(err)
	}
	return len(ds) + len(as) + len(ps) + len(rs)
}
