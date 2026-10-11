package harvest_test

import (
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// hot is an utterance the session answered as a hot phrase, without the
// model (ADR-0064).
func hot(text, word, audio string) journal.Record {
	r := heard(text, audio)
	r.Fields["hot_word"] = word
	return r
}

// stopCut is cutTurn with its barge-in admitted on a hot partial.
func stopCut(partial string) []journal.Record {
	cut := cutTurn()
	cut[4].Fields["hot_word"] = partial
	return cut
}

// Alice says "stop" over the albums and nothing more. A stop has no chosen
// side, so no pair is cut from it; it is counted, kept in the log, and not
// mistaken for a gate that let something through uncorrected.
//
// verifies SPEC §9.1
func TestAStopIsCountedAndNeverPaired(t *testing.T) {
	for name, said := range map[string]journal.Record{
		"stop":       hot("Stop.", "stop", "blob://mic/2"),
		"never mind": hot("Never mind.", "never_mind", "blob://mic/2"),
	} {
		t.Run(name, func(t *testing.T) {
			res := scan(t, conversation(t, versions(), concat(
				[]journal.Record{opened("kitchen")}, stopCut("stop"), []journal.Record{said},
				correctedTurn(), []journal.Record{closed("model_ended")},
			)))
			if len(res.Pairs) != 0 || res.Hushed != 1 || res.Uncorrected != 0 {
				t.Errorf("pairs = %d hushed = %d uncorrected = %d, want 0, 1, 0", len(res.Pairs), res.Hushed, res.Uncorrected)
			}
		})
	}
}

// Alice talks over the albums to hear them again, and the kitchen repeats
// what she heard. The answer was not wrong, only unheard, so it is no
// pair; and the repeat's words were chosen by no turn, so the "say that
// again" turn said nothing a later pair could carry as its own.
//
// verifies SPEC §9.1
func TestARepeatIsNoCorrectionAndItsWordsAreNobodysChoice(t *testing.T) {
	again := []journal.Record{
		hot("Say that again.", "repeat", "blob://mic/2"),
		record(journal.KindToolCalled, "", "tool", "speak", "call_id", "rp_5d1e", "args_json", `{"text":"I found three","mode":"queue","repeats":true}`),
		record(journal.KindSpeechSpoken, "blob://tts/rp_5d1e", "text", "I found three", "frames_played", "2080", "call_id", "rp_5d1e"),
		record(journal.KindToolResult, "", "call_id", "rp_5d1e", "outcome", "ok"),
	}
	res := scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, stopCut("repeat"), again,
		correctedTurn(), []journal.Record{closed("model_ended")},
	)))
	if len(res.Pairs) != 0 || res.Hushed != 1 {
		t.Fatalf("pairs = %d hushed = %d, want 0 and 1", len(res.Pairs), res.Hushed)
	}
	turn := res.Turns[1]
	if turn.Said != "" || len(turn.Calls) != 0 || len(turn.Audio) != 0 {
		t.Errorf("the repeat's turn = %+v, want nothing chosen", turn)
	}
	if prompt := res.Turns[2].Prompt; len(prompt) < 2 || prompt[len(prompt)-2].Content != "Say that again." {
		t.Errorf("the next turn's prompt = %+v, want the repeat request in it", prompt)
	}
}

// The barge-in was the hot partial "stop", but Alice went on: "stop the
// music in the kitchen" is a correction the model answered, and is paired
// as any correction is.
//
// verifies SPEC §9.1
func TestAStopThatWentOnToAskForSomethingIsPaired(t *testing.T) {
	res := scan(t, conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, stopCut("stop"), correctedTurn(), []journal.Record{closed("model_ended")},
	)))
	if p := onePair(t, res); p.Heard != "just the first one" || res.Hushed != 0 {
		t.Errorf("pair heard %q, hushed = %d; want the correction paired", p.Heard, res.Hushed)
	}
}
