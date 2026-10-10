package curation_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// newStore builds an empty store. Postgres reuses one database, so its
// factory must hand back a clean table.
type newStore func(t *testing.T) curation.Store

// storeConformance is one suite run against every Store, like the journal's:
// a behaviour MemStore has and PgStore lacks is a bug, not a backend detail.
var storeConformance = map[string]func(*testing.T, curation.Store){
	"an unknown pair has no decision": func(t *testing.T, s curation.Store) {
		_, ok, err := s.Get(context.Background(), "conv-1/41")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if ok {
			t.Error("got a decision for a pair nobody reviewed")
		}
	},

	"every decision field round-trips": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		want := curation.Decision{
			PairID:         "conv-1/41",
			ConversationID: "conv-1",
			Status:         curation.StatusAccepted,
			Chosen:         "Dentist at nine. That's it.",
			Unfixed:        true,
			Reason:         "",
			Prev:           curation.StatusEdited,
			DecidedAt:      time.Date(2026, 10, 8, 22, 4, 53, 123456000, time.UTC),
		}
		if err := s.Put(ctx, want); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, ok, err := s.Get(ctx, want.PairID)
		if err != nil || !ok {
			t.Fatalf("get: ok=%v err=%v", ok, err)
		}
		if got != want {
			t.Errorf("decision = %+v, want %+v", got, want)
		}
	},

	"the last decision wins": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		d := decision("conv-1/41", curation.StatusAccepted)
		if err := s.Put(ctx, d); err != nil {
			t.Fatalf("put: %v", err)
		}
		d.Status, d.Reason = curation.StatusDiscarded, "Duplicate"
		d.DecidedAt = d.DecidedAt.Add(time.Minute)
		if err := s.Put(ctx, d); err != nil {
			t.Fatalf("put again: %v", err)
		}
		got, _, err := s.Get(ctx, d.PairID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got != d {
			t.Errorf("decision = %+v, want the second write %+v", got, d)
		}
	},

	"a conversation's decisions come back keyed by pair": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		mine := decision("conv-1/41", curation.StatusAccepted)
		other := decision("conv-2/7", curation.StatusEdited)
		other.ConversationID = "conv-2"
		for _, d := range []curation.Decision{mine, other} {
			if err := s.Put(ctx, d); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
		got, err := s.ForConversation(ctx, "conv-1")
		if err != nil {
			t.Fatalf("for conversation: %v", err)
		}
		if len(got) != 1 || got[mine.PairID] != mine {
			t.Errorf("decisions = %v, want only %+v", got, mine)
		}
	},

	"a decision without a pair id is refused": func(t *testing.T, s curation.Store) {
		err := s.Put(context.Background(), decision("", curation.StatusAccepted))
		if err == nil {
			t.Error("stored a decision no pair can claim")
		}
	},

	"a decision with an unknown prev status is refused": func(t *testing.T, s curation.Store) {
		d := decision("conv-1/41", curation.StatusAccepted)
		d.Prev = "exploded"
		if err := s.Put(context.Background(), d); err == nil {
			t.Error("stored a prev status undo could never restore")
		}
	},

	"a decision with an unknown status is refused": func(t *testing.T, s curation.Store) {
		err := s.Put(context.Background(), decision("conv-1/41", "unreviewed"))
		if err == nil {
			t.Error("stored a status the Curate flow never produces")
		}
	},

	"a deleted decision is gone, and deleting nothing is fine": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		if err := s.Delete(ctx, "conv-1/41"); err != nil {
			t.Fatalf("delete absent: %v", err)
		}
		if err := s.Put(ctx, decision("conv-1/41", curation.StatusAccepted)); err != nil {
			t.Fatalf("put: %v", err)
		}
		if err := s.Delete(ctx, "conv-1/41"); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, ok, err := s.Get(ctx, "conv-1/41"); err != nil || ok {
			t.Errorf("after delete: ok=%v err=%v, want gone", ok, err)
		}
	},
}

// annotationConformance and promotionConformance hold every store to the
// same answers about labelled turns and promoted takes.
var annotationConformance = map[string]func(*testing.T, curation.Store){
	"an annotation's every field round-trips": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		want := zeppelinAnnotation()
		if err := s.PutAnnotation(ctx, want); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Annotations(ctx, want.ConversationID)
		if err != nil {
			t.Fatalf("annotations: %v", err)
		}
		if !reflect.DeepEqual(got, map[uint64]curation.Annotation{want.Seq: want}) {
			t.Errorf("annotations = %+v, want only %+v", got, want)
		}
	},

	"the last annotation of a turn wins": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		a := zeppelinAnnotation()
		if err := s.PutAnnotation(ctx, a); err != nil {
			t.Fatalf("put: %v", err)
		}
		a = a.Toggle(curation.LabelTooSlow)
		a.ShouldHave = "Asked which album, then stopped."
		if err := s.PutAnnotation(ctx, a); err != nil {
			t.Fatalf("put again: %v", err)
		}
		got, err := s.Annotations(ctx, a.ConversationID)
		if err != nil {
			t.Fatalf("annotations: %v", err)
		}
		if !reflect.DeepEqual(got[a.Seq], a) {
			t.Errorf("annotation = %+v, want the second write %+v", got[a.Seq], a)
		}
	},

	"a note with no label is still an annotation": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		a := zeppelinAnnotation()
		a.Labels = nil
		if err := s.PutAnnotation(ctx, a); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Annotations(ctx, a.ConversationID)
		if err != nil {
			t.Fatalf("annotations: %v", err)
		}
		if !reflect.DeepEqual(got[a.Seq], a) {
			t.Errorf("annotation = %+v, want %+v", got[a.Seq], a)
		}
	},

	"taking every label and the note off unannotates the turn": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		a := zeppelinAnnotation()
		if err := s.PutAnnotation(ctx, a); err != nil {
			t.Fatalf("put: %v", err)
		}
		a.Labels, a.ShouldHave = nil, "  "
		if err := s.PutAnnotation(ctx, a); err != nil {
			t.Fatalf("put empty: %v", err)
		}
		got, err := s.Annotations(ctx, a.ConversationID)
		if err != nil {
			t.Fatalf("annotations: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("annotations = %+v, want none", got)
		}
	},

	"annotations stay with their conversation": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		mine, other := zeppelinAnnotation(), zeppelinAnnotation()
		other.ConversationID = "conv-1840-living_room"
		for _, a := range []curation.Annotation{mine, other} {
			if err := s.PutAnnotation(ctx, a); err != nil {
				t.Fatalf("put: %v", err)
			}
		}
		got, err := s.Annotations(ctx, mine.ConversationID)
		if err != nil {
			t.Fatalf("annotations: %v", err)
		}
		if len(got) != 1 || got[mine.Seq].ConversationID != mine.ConversationID {
			t.Errorf("annotations = %+v, want only Alice's morning", got)
		}
	},

	"a label outside the vocabulary is refused": func(t *testing.T, s curation.Store) {
		a := zeppelinAnnotation()
		a.Labels = append(a.Labels, "rude")
		if err := s.PutAnnotation(context.Background(), a); err == nil {
			t.Error("stored a label SPEC §9.2 does not have")
		}
	},

	"an annotation that names no turn is refused": func(t *testing.T, s curation.Store) {
		a := zeppelinAnnotation()
		a.Seq = 0
		if err := s.PutAnnotation(context.Background(), a); err == nil {
			t.Error("stored an annotation no turn can claim")
		}
	},
}

var promotionConformance = map[string]func(*testing.T, curation.Store){
	"a promotion's every field round-trips": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		want := zeppelinPromotion()
		if err := s.PutPromotion(ctx, want); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Promotions(ctx, want.ConversationID)
		if err != nil {
			t.Fatalf("promotions: %v", err)
		}
		if !reflect.DeepEqual(got, map[uint64]curation.Promotion{want.Seq: want}) {
			t.Errorf("promotions = %+v, want only %+v", got, want)
		}
	},

	"a take that only speaks keeps no calls": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		p := zeppelinPromotion()
		p.Calls = []curation.Call{}
		if err := s.PutPromotion(ctx, p); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Promotions(ctx, p.ConversationID)
		if err != nil {
			t.Fatalf("promotions: %v", err)
		}
		if got[p.Seq].Calls != nil {
			t.Errorf("calls = %#v, want nil", got[p.Seq].Calls)
		}
	},

	"promoting a turn again replaces the take": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		p := zeppelinPromotion()
		if err := s.PutPromotion(ctx, p); err != nil {
			t.Fatalf("put: %v", err)
		}
		p.Speech, p.Versions.Prompt = "Three albums. Led Zeppelin one?", "sys@edited-2"
		p.PromotedAt = p.PromotedAt.Add(time.Minute)
		if err := s.PutPromotion(ctx, p); err != nil {
			t.Fatalf("put again: %v", err)
		}
		got, err := s.Promotions(ctx, p.ConversationID)
		if err != nil {
			t.Fatalf("promotions: %v", err)
		}
		if !reflect.DeepEqual(got[p.Seq], p) {
			t.Errorf("promotion = %+v, want the second write %+v", got[p.Seq], p)
		}
	},

	"a take that neither says nor calls anything is refused": func(t *testing.T, s curation.Store) {
		p := zeppelinPromotion()
		p.Speech, p.Calls = " ", nil
		if err := s.PutPromotion(context.Background(), p); err == nil {
			t.Error("stored a chosen side that does nothing")
		}
	},

	"a take that only calls is kept": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		p := garageCheckPromotion()
		if err := s.PutPromotion(ctx, p); err != nil {
			t.Fatalf("put: %v", err)
		}
		got, err := s.Promotions(ctx, p.ConversationID)
		if err != nil {
			t.Fatalf("promotions: %v", err)
		}
		if !reflect.DeepEqual(got[p.Seq], p) {
			t.Errorf("promotion = %+v, want %+v", got[p.Seq], p)
		}
	},
}

var rerunConformance = map[string]func(*testing.T, curation.Store){
	"a re-run's every field round-trips": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		want := zeppelinRerun()
		id, err := s.AddRerun(ctx, want)
		if err != nil {
			t.Fatalf("add: %v", err)
		}
		if id == 0 {
			t.Fatal("the store gave the re-run no id")
		}
		want.ID = id
		got, err := s.Reruns(ctx, want.ConversationID)
		if err != nil {
			t.Fatalf("reruns: %v", err)
		}
		if !reflect.DeepEqual(got, []curation.Rerun{want}) {
			t.Errorf("reruns = %+v, want only %+v", got, want)
		}
	},

	"every re-run is kept, newest first": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		first, second, third := zeppelinRerun(), zeppelinRerun(), zeppelinRerun()
		second.Versions.ToolSchema, second.RanAt = "tools@edited", first.RanAt.Add(time.Minute)
		third.Versions.Model, third.RanAt = "qwen3-14b@1", second.RanAt // same instant: the later add is newer
		var ids []uint64
		for _, r := range []curation.Rerun{first, second, third} {
			id, err := s.AddRerun(ctx, r)
			if err != nil {
				t.Fatalf("add: %v", err)
			}
			ids = append(ids, id)
		}
		got, err := s.Reruns(ctx, first.ConversationID)
		if err != nil {
			t.Fatalf("reruns: %v", err)
		}
		var order []uint64
		for _, r := range got {
			order = append(order, r.ID)
		}
		if want := []uint64{ids[2], ids[1], ids[0]}; !reflect.DeepEqual(order, want) {
			t.Errorf("re-runs came back %v, want newest first %v", order, want)
		}
	},

	"re-runs stay with their conversation": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		mine, other := zeppelinRerun(), zeppelinRerun()
		other.ConversationID = "conv-1840-living_room"
		for _, r := range []curation.Rerun{mine, other} {
			if _, err := s.AddRerun(ctx, r); err != nil {
				t.Fatalf("add: %v", err)
			}
		}
		got, err := s.Reruns(ctx, mine.ConversationID)
		if err != nil {
			t.Fatalf("reruns: %v", err)
		}
		if len(got) != 1 || got[0].ConversationID != mine.ConversationID {
			t.Errorf("reruns = %+v, want only Alice's morning", got)
		}
	},

	"a re-run's take that only speaks keeps no calls": func(t *testing.T, s curation.Store) {
		ctx := context.Background()
		r := zeppelinRerun()
		r.Takes[1].Calls = []curation.Call{}
		if _, err := s.AddRerun(ctx, r); err != nil {
			t.Fatalf("add: %v", err)
		}
		got, err := s.Reruns(ctx, r.ConversationID)
		if err != nil || len(got) != 1 {
			t.Fatalf("reruns: %v, %v", got, err)
		}
		if got[0].Takes[1].Calls != nil {
			t.Errorf("calls = %#v, want nil", got[0].Takes[1].Calls)
		}
	},

	"a re-run nothing could be promoted from is refused": func(t *testing.T, s curation.Store) {
		for name, edit := range map[string]func(*curation.Rerun){
			"no conversation":        func(r *curation.Rerun) { r.ConversationID = "" },
			"no turn ran":            func(r *curation.Rerun) { r.Takes = nil },
			"a take with no turn":    func(r *curation.Rerun) { r.Takes[0].Seq = 0 },
			"no tool-schema version": func(r *curation.Rerun) { r.Versions.ToolSchema = "" },
		} {
			r := zeppelinRerun()
			edit(&r)
			if _, err := s.AddRerun(context.Background(), r); err == nil {
				t.Errorf("%s: stored %+v", name, r)
			}
		}
	},
}

// zeppelinRerun is Alice's morning asked again under the brief prompt: the
// list turn changes, the follow-up answers as it did.
func zeppelinRerun() curation.Rerun {
	return curation.Rerun{
		ConversationID: "conv-0853-kitchen",
		Versions:       journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@7"},
		SystemPrompt:   "You are a voice assistant in a home.\nWhen there are several results, say how many, offer the first, and stop.",
		ToolSchema:     mediaSearchOnly,
		Takes: []curation.Take{
			{
				Seq: 2, Speech: "I found three albums. Want Led Zeppelin one?", Finish: "stop",
				Calls: []curation.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","media_type":"album","limit":5}`}},
			},
			{
				Seq: 10, Speech: "Playing Led Zeppelin one.", Finish: "stop",
				Calls: []curation.Call{{Tool: "ha_call_service", Args: `{"domain":"media_player","service":"play_media"}`}},
			},
		},
		RanAt: time.Date(2025, 10, 9, 22, 43, 5, 500000000, time.UTC),
	}
}

// mediaSearchOnly is a tool schema cut down to the search, as Replay's
// editor holds one.
const mediaSearchOnly = `[
  {
    "type": "function",
    "function": {
      "name": "media_search",
      "description": "Search the media library. Takes several seconds to return.",
      "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}
    }
  }
]`

// garageCheckPromotion is Teagan's garage question re-run: it checks the
// contact sensor and says nothing until the sensor answers.
func garageCheckPromotion() curation.Promotion {
	return curation.Promotion{
		ConversationID: "conv-0930-office",
		Seq:            2,
		Calls:          []curation.Call{{Tool: "ha_get_state", Args: `{"entity_id":"binary_sensor.garage_door_contact"}`}},
		Versions:       journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@7"},
		SystemPrompt:   "You are a voice assistant in a home.\nWhen there are several results, say how many, offer the first, and stop.",
		PromotedAt:     time.Date(2025, 10, 9, 22, 47, 12, 0, time.UTC),
	}
}

// zeppelinAnnotation is Alice's album list, labelled by the reviewer.
func zeppelinAnnotation() curation.Annotation {
	return curation.Annotation{
		ConversationID: "conv-0853-kitchen",
		Seq:            2,
		Labels:         []curation.Label{curation.LabelMisunderstoodIntent, curation.LabelSpokeWhenShouldnt},
		ShouldHave:     "I found three albums. Want Led Zeppelin one?",
		AnnotatedAt:    time.Date(2025, 10, 9, 22, 41, 7, 250000000, time.UTC),
	}
}

// zeppelinPromotion is the same turn re-run under a briefer prompt.
func zeppelinPromotion() curation.Promotion {
	return curation.Promotion{
		ConversationID: "conv-0853-kitchen",
		Seq:            2,
		Speech:         "I found three albums. Want Led Zeppelin one?",
		Calls:          []curation.Call{{Tool: "media_search", Args: `{"query":"Led Zeppelin","media_type":"album","limit":5}`}},
		Versions:       journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@edited", ToolSchema: "tools@7"},
		SystemPrompt:   "You are a voice assistant in a home.\nWhen there are several results, say how many, offer the first, and stop.",
		ToolSchema:     mediaSearchOnly,
		PromotedAt:     time.Date(2025, 10, 9, 22, 44, 30, 0, time.UTC),
	}
}

// decision is a minimal valid write; its DecidedAt is within StoredClockResolution.
func decision(pairID string, status curation.Status) curation.Decision {
	return curation.Decision{
		PairID:         pairID,
		ConversationID: "conv-1",
		Status:         status,
		Chosen:         "chosen text",
		DecidedAt:      time.Unix(1_760_000_000, 0).UTC(),
	}
}

func runStoreConformance(t *testing.T, open newStore) {
	t.Helper()
	for _, suite := range []map[string]func(*testing.T, curation.Store){storeConformance, annotationConformance, promotionConformance, rerunConformance} {
		for name, run := range suite {
			t.Run(name, func(t *testing.T) { run(t, open(t)) })
		}
	}
}

// verifies SPEC §9.2
func TestMemStoreConformance(t *testing.T) {
	runStoreConformance(t, func(*testing.T) curation.Store { return curation.NewMemStore() })
}
