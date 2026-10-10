package main

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

// dentist is what Alan asked the kitchen to remember last week.
var dentist = memory.Memory{
	ID: "m_8c41d2e7", Person: "alan", Fact: "Dentist is Dr. Okafor, Tuesdays at nine.",
	ConversationID: "c-last-week", CallID: "call_r1", At: epoch,
}

// Alan asks the kitchen about his dentist, and a friend over for dinner
// chimes in to have that forgotten. The friend's voice matched nobody, so
// the daemon hears a guest: the turn is not told Alan's memories, the
// forget is refused before it runs, Alan's dentist is still remembered, and
// the log says what each voice matched.
//
// verifies SPEC §5
func TestAFriendAtDinnerCannotForgetAlansDentist(t *testing.T) {
	memories := memory.NewMemStore()
	if err := memories.Remember(context.Background(), dentist); err != nil {
		t.Fatal(err)
	}
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		if last.Kind == journal.EntryHeard && last.Text == "oh just forget the dentist thing" {
			// A model that saw the id earlier in the evening and uses it.
			return []session.Action{session.ToolCall{ID: "call_f1", Tool: "forget", Args: `{"memory_id":"` + dentist.ID + `"}`}, done}
		}
		return []session.Action{done}
	}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.Memories = memories
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("when is my dentist appointment", alan))
	r.utter(t, dev, r.line("oh just forget the dentist thing", stranger))

	res := r.store.awaitKind(t, journal.KindToolResult, 1)
	if want := map[string]string{"call_id": "call_f1", "outcome": "error", "result_json": `{"error":"unidentified_speaker"}`}; !reflect.DeepEqual(res.Fields, want) {
		t.Errorf("forget = %v, want refused as unidentified", res.Fields)
	}
	if got, _ := memories.Recall(context.Background(), "alan", memory.RecallLimit); len(got) != 1 || got[0].ID != dentist.ID {
		t.Errorf("Alan now remembers %+v, want the dentist kept", got)
	}

	asks := eng.heard()
	if len(asks) < 2 {
		t.Fatalf("the model was asked %d times, want Alan's turn and the guest's", len(asks))
	}
	if alans := asks[0]; alans.Speaker != "alan" || len(alans.Memories) != 1 {
		t.Errorf("Alan's turn was asked as %q told %+v", alans.Speaker, alans.Memories)
	}
	if guest := asks[1]; guest.Speaker != "" || len(guest.Memories) != 0 {
		t.Errorf("the guest's turn was asked as %q told %+v, want a guest told nothing", guest.Speaker, guest.Memories)
	}

	var matched []string
	for _, e := range r.store.ofKind(journal.KindUtteranceTranscribed) {
		matched = append(matched, e.Fields["speaker_match"])
	}
	if want := []string{"identified", "below_threshold"}; !slices.Equal(matched, want) {
		t.Errorf("speaker_match = %q, want %q", matched, want)
	}
	recalls := r.store.ofKind(journal.KindMemoryRecalled)
	if len(recalls) != 2 || recalls[0].Fields["person"] != "alan" || recalls[1].Fields["person"] != "" || recalls[1].Fields["memories_json"] != "[]" {
		t.Errorf("memory_recalled = %v, want Alan's then the guest's empty one", recalls)
	}
}

// The kitchen is answering Alan when the television talks over it, loud and
// wordy and nobody's. The gate refuses to stop speech for it, the answer
// plays out, and the television is never asked about: Alan's next question
// is the only other thing the model hears.
//
// verifies SPEC §4.3, §5
func TestTheTelevisionTalkingOverTheKitchenIsNotAnswered(t *testing.T) {
	const answer = "Here is some jazz for the kitchen."
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	eng := &scriptEngine{decide: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Text == "put on some jazz" {
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: answer, Last: true}, done}
		}
		return []session.Action{done}
	}}
	r := newRig(t, inventory(), func(d *deps) { d.engine = eng })
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("put on some jazz", alan))
	dev.AwaitTTS(t, 2*len(answer))

	// Long enough for a partial, so the gate rules before the pause ends it.
	tv := r.line("and now the weather for the weekend", stranger)
	for sent := 0; sent < 2*stt.DefaultPartialEvery; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, voice(tv, chunkBytes))
	}
	if rej := r.store.awaitKind(t, journal.KindBargeInRejected, 1); rej.Fields["stage"] != "speaker_id" {
		t.Errorf("barge_in_rejected = %v, want the speaker stage", rej.Fields)
	}
	for sent := 0; sent < silence; sent += chunkBytes {
		dev.SendMic(t, bridge.ChannelAEC, quiet(chunkBytes))
	}
	dev.PlayAll(t)
	if spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1); spoken.Fields["text"] != answer {
		t.Errorf("speech_spoken = %v, want the whole answer", spoken.Fields)
	}

	r.utter(t, dev, r.line("a bit quieter please", alan))
	r.store.awaitKind(t, journal.KindUtteranceTranscribed, 2)
	var heard []string
	for _, e := range r.store.ofKind(journal.KindUtteranceTranscribed) {
		heard = append(heard, e.Fields["text"])
	}
	if want := []string{"put on some jazz", "a bit quieter please"}; !slices.Equal(heard, want) {
		t.Errorf("heard %q, want %q: the television is not a turn", heard, want)
	}
	await(t, "Alan's second turn", func() bool { return len(eng.heard()) == 2 })
	for _, in := range eng.heard() {
		if in.Speaker != "alan" {
			t.Errorf("the model was asked %q as %q, want only Alan", in.Text, in.Speaker)
		}
	}
	if n := len(r.store.ofKind(journal.KindBargeInDetected)); n != 0 {
		t.Errorf("the television stopped speech %d times", n)
	}
}
