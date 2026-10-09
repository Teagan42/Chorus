package journal_test

import (
	"reflect"
	"testing"

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
