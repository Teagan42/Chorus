package journal_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

// Teagan's oat milk, and the wifi password Alice told the house about.
var teagansMemories = []journal.Memory{
	{ID: "m_3f9c2a10", Person: "teagan", Fact: "Takes oat milk in coffee."},
	{ID: "m_77d01b2e", Person: "alice", Fact: "The guest wifi password is on the fridge.", Shareable: true},
}

// The model is told what the log says it was told, not what the store holds
// now: a memory forgotten tomorrow was still recalled today.
//
// verifies SPEC §5, §8
func TestAReplayRecallsWhatTheTurnWasGiven(t *testing.T) {
	recalled := func(person string, ms []journal.Memory) journal.Event {
		return ev(journal.KindMemoryRecalled, map[string]string{"person": person, "memories_json": journal.EncodeMemories(ms)})
	}
	st := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		recalled("teagan", teagansMemories),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "how do I take my coffee", "speaker_id": "teagan"}),
	})
	if !reflect.DeepEqual(st.Recalled, teagansMemories) || st.RecalledFor != "teagan" {
		t.Errorf("recalled for %q: %+v, want Teagan's two", st.RecalledFor, st.Recalled)
	}

	// Alice joins the conversation: her memories replace Teagan's, and the
	// oat milk is not hers to be told about.
	st = reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		recalled("teagan", teagansMemories),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "what's the wifi password", "speaker_id": "alice"}),
		recalled("alice", teagansMemories[1:]),
	})
	if !reflect.DeepEqual(st.Recalled, teagansMemories[1:]) || st.RecalledFor != "alice" {
		t.Errorf("recalled for %q: %+v, want only the shared password", st.RecalledFor, st.Recalled)
	}

	st = reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "alan"}),
		recalled("alan", nil),
	})
	if st.Recalled != nil || st.RecalledFor != "alan" {
		t.Errorf("recalled for %q: %+v, want Alan with nothing remembered", st.RecalledFor, st.Recalled)
	}
}

// A memories_json that does not decode is a corrupt log, not an empty memory.
//
// verifies SPEC §8
func TestUnreadableMemoriesFailTheReplay(t *testing.T) {
	_, err := journal.Reduce(journal.State{}, ev(journal.KindMemoryRecalled, map[string]string{
		"person": "teagan", "memories_json": `[{"id":"m_3f9c2a10","fact":`,
	}))
	if err == nil {
		t.Error("an unreadable recall was folded")
	}
}

// Teagan asked from the garage about the leaking water heater; Alan joined to
// say the plumber comes Thursday. The summary is kept for both of them, and
// whoever spoke without being recognised is nobody's to keep it for.
//
// verifies SPEC §5
func TestEveryIdentifiedSpeakerIsAParticipant(t *testing.T) {
	st := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "garage", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "the water heater is leaking again", "speaker_id": "teagan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "the plumber comes Thursday", "speaker_id": "alan"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "is that the one from last time"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "yes, same one", "speaker_id": "teagan"}),
	})
	if want := []string{"teagan", "alan"}; !reflect.DeepEqual(st.Participants, want) {
		t.Errorf("participants = %v, want %v", st.Participants, want)
	}

	guest := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "front door"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "is anyone home"}),
	})
	if guest.Participants != nil {
		t.Errorf("a guest's conversation has participants %v", guest.Participants)
	}
}

// A turn is told the time the utterance was logged, so asking again from the
// log tells the model the same "now" it was told the first time.
//
// verifies SPEC §5, §8
func TestATurnIsToldWhenItWasHeard(t *testing.T) {
	first := time.Date(2026, 10, 8, 18, 4, 0, 0, time.FixedZone("PDT", -7*3600))
	second := first.Add(90 * time.Second)
	heard := func(at time.Time, text string) journal.Event {
		e := ev(journal.KindUtteranceTranscribed, map[string]string{"text": text, "speaker_id": "teagan"})
		e.At = at
		return e
	}
	st := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		heard(first, "start a timer for the pasta"),
		heard(second, "how long is left"),
	})
	if !st.HeardAt.Equal(second) {
		t.Errorf("heard at %v, want the second ask's %v", st.HeardAt, second)
	}
}

// What the turn was told about Teagan's earlier conversations is read back
// from the log, and a log from before summaries tells the model of none.
//
// verifies SPEC §5, §8
func TestAReplayRecallsTheConversationsTheTurnWasGiven(t *testing.T) {
	yesterday := []journal.Summary{
		{ConversationID: "conv-garage-0812", At: time.Date(2026, 10, 8, 18, 4, 0, 0, time.UTC), Text: "Teagan asked whether the garage door was closed; it was open and Chorus closed it."},
		{ConversationID: "conv-kitchen-0731", At: time.Date(2026, 10, 8, 7, 31, 0, 0, time.UTC), Text: "Teagan set a ten minute timer for the eggs."},
	}
	st := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindMemoryRecalled, map[string]string{
			"person": "teagan", "memories_json": journal.EncodeMemories(nil), "summaries_json": journal.EncodeSummaries(yesterday),
		}),
	})
	if !reflect.DeepEqual(st.RecalledSummaries, yesterday) {
		t.Errorf("recalled summaries = %+v, want yesterday's two", st.RecalledSummaries)
	}

	before := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "kitchen", "speaker_id": "teagan"}),
		ev(journal.KindMemoryRecalled, map[string]string{"person": "teagan", "memories_json": journal.EncodeMemories(teagansMemories)}),
	})
	if before.RecalledSummaries != nil || len(before.Recalled) != 2 {
		t.Errorf("an older log recalled summaries %+v and memories %+v", before.RecalledSummaries, before.Recalled)
	}

	_, err := journal.Reduce(journal.State{}, ev(journal.KindMemoryRecalled, map[string]string{
		"person": "teagan", "memories_json": "[]", "summaries_json": `[{"conversation_id":`,
	}))
	if err == nil {
		t.Error("unreadable summaries were folded")
	}
}

// The summary the model wrote at the end is part of the conversation's state,
// and a failed attempt leaves none.
//
// verifies SPEC §5
func TestTheConversationKeepsItsSummary(t *testing.T) {
	st := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "front door", "speaker_id": "alice"}),
		ev(journal.KindUtteranceTranscribed, map[string]string{"text": "did the package come", "speaker_id": "alice"}),
		ev(journal.KindSessionClosed, map[string]string{"reason": "model_ended", "satellite": "front door"}),
		ev(journal.KindConversationSummarized, map[string]string{
			"people_json": `["alice"]`, "summary": "Alice asked whether the package had arrived; the front door camera showed it on the step.",
		}),
	})
	if st.Summary != "Alice asked whether the package had arrived; the front door camera showed it on the step." {
		t.Errorf("summary = %q", st.Summary)
	}

	failed := reduceAll(t, []journal.Event{
		ev(journal.KindSessionOpened, map[string]string{"satellite": "front door", "speaker_id": "alice"}),
		ev(journal.KindConversationSummarized, map[string]string{"people_json": `["alice"]`, "error": "context deadline exceeded"}),
	})
	if failed.Summary != "" {
		t.Errorf("a failed summary left %q", failed.Summary)
	}
}
