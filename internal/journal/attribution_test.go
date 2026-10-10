package journal_test

import (
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// fold reduces events in order, numbering them as the store would.
func fold(t *testing.T, events ...journal.Event) journal.State {
	t.Helper()
	var (
		s   journal.State
		err error
	)
	for i, e := range events {
		e.Seq = uint64(i + 1)
		if s, err = journal.Reduce(s, e); err != nil {
			t.Fatalf("reduce %s: %v", e.Kind, err)
		}
	}
	return s
}

func heard(text, speaker, match string) journal.Event {
	f := map[string]string{"text": text, "speaker_id": speaker}
	if match != "" {
		f["speaker_match"] = match
	}
	return journal.Event{Kind: journal.KindUtteranceTranscribed, AudioRef: "mic/" + text, Fields: f}
}

// Alice asks the kitchen about her dentist appointment, and a friend over for
// dinner chimes in. The friend's voice was judged and matched nobody, so the
// turn is a guest's: the reducer must not hand it to Alice, or the friend
// is told Alice's memories and can forget them as her.
//
// verifies SPEC §5
func TestAVoiceThatMatchedNobodyIsAGuestNotWhoeverSpokeBefore(t *testing.T) {
	for _, match := range []string{"below_threshold", "ambiguous"} {
		t.Run(match, func(t *testing.T) {
			s := fold(t,
				journal.Event{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alice"}},
				heard("when is my dentist appointment", "alice", "identified"),
				heard("ooh can you remind me too", "", match),
			)
			if s.Speaker != "" {
				t.Errorf("Speaker = %q, want a guest", s.Speaker)
			}
			want := []journal.Entry{
				{Kind: journal.EntryHeard, Text: "when is my dentist appointment", Speaker: "alice"},
				{Kind: journal.EntryHeard, Text: "ooh can you remind me too"},
			}
			if !reflect.DeepEqual(s.Dialogue, want) {
				t.Errorf("Dialogue = %+v, want the friend's line attributed to nobody", s.Dialogue)
			}
			if !reflect.DeepEqual(s.Participants, []string{"alice"}) {
				t.Errorf("Participants = %q, want Alice alone", s.Participants)
			}
		})
	}
}

// When Alice answers her friend, the conversation is hers again: a guest's
// turn in between does not lose her.
//
// verifies SPEC §5
func TestTheIdentifiedSpeakerTakesTheConversationBackFromAGuest(t *testing.T) {
	s := fold(t,
		journal.Event{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alice"}},
		heard("when is my dentist appointment", "alice", "identified"),
		heard("ooh can you remind me too", "", "below_threshold"),
		heard("and set one for me at nine", "alice", "identified"),
	)
	if s.Speaker != "alice" {
		t.Errorf("Speaker = %q, want alice back", s.Speaker)
	}
}

// Nothing judged the voice: speaker identification is not configured, or the
// embedder was down for this utterance. That is not evidence of someone
// else, so the turn stays with the current speaker, as every log from before
// speaker_match was recorded does.
//
// verifies SPEC §5
func TestAnUnjudgedVoiceKeepsTheCurrentSpeaker(t *testing.T) {
	for name, match := range map[string]string{"unjudged": "", "nobody enrolled": "nobody_enrolled"} {
		t.Run(name, func(t *testing.T) {
			s := fold(t,
				journal.Event{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "office", "speaker_id": "teagan"}},
				heard("turn the office lights down", "", match),
			)
			if s.Speaker != "teagan" {
				t.Errorf("Speaker = %q, want teagan kept", s.Speaker)
			}
			if got := s.Dialogue[0].Speaker; got != "teagan" {
				t.Errorf("Dialogue speaker = %q, want teagan", got)
			}
		})
	}
}

// A guest's turn records that it recalled nothing, and that clears what the
// person before was told: the next ask must not carry Alice's memories.
//
// verifies SPEC §5
func TestAGuestsEmptyRecallClearsWhatThePersonBeforeWasTold(t *testing.T) {
	dentist := []journal.Memory{{ID: "m-41", Person: "alice", Fact: "Dentist is Dr. Okafor on Tuesdays."}}
	s := fold(t,
		journal.Event{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alice"}},
		heard("when is my dentist appointment", "alice", "identified"),
		journal.Event{Kind: journal.KindMemoryRecalled, Fields: map[string]string{"person": "alice", "memories_json": journal.EncodeMemories(dentist)}},
		heard("ooh can you remind me too", "", "below_threshold"),
		journal.Event{Kind: journal.KindMemoryRecalled, Fields: map[string]string{"person": "", "memories_json": journal.EncodeMemories(nil)}},
	)
	if len(s.Recalled) != 0 || s.RecalledFor != "" {
		t.Errorf("recalled for %q: %+v, want nothing for the guest", s.RecalledFor, s.Recalled)
	}
}
