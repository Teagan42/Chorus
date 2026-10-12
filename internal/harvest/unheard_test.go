package harvest_test

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
)

// earLine is the ear's canned line as the session says it after an utterance
// STT could not decode: a speak call marked canned and unheard, which no
// failure event names (ADR-0069).
func earLine(said ...journal.Record) []journal.Record {
	return concat([]journal.Record{
		record(journal.KindToolCalled, "", "tool", "speak", "call_id", "cn_ear1", "args_json",
			`{"text":"Sorry, my hearing isn't working right now. Try again in a minute.","mode":"queue","canned":true,"unheard":true}`),
	}, said, []journal.Record{
		record(journal.KindToolResult, "", "call_id", "cn_ear1", "outcome", "ok"),
	})
}

func earSpoken() journal.Record {
	return record(journal.KindSpeechSpoken, "blob://tts/cn_ear1",
		"text", "Sorry, my hearing isn't working right now. Try again in a minute.", "frames_played", "64000", "call_id", "cn_ear1")
}

// Alan asks for the porch light and hears it done; then the ear fails on
// what he says next, and the kitchen apologises. The apology answers words
// nothing decoded: it is nobody's turn, so it is not the porch light's.
//
// verifies SPEC §9.1
func TestTheEarsApologyIsNoPartOfTheTurnBeforeIt(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{
			opened("kitchen"),
			heard("turn on the porch light", "blob://mic/1"),
			speak("s1"),
			spoken("The porch light is on.", "blob://tts/s1"),
			record(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok"),
			completed(),
		},
		earLine(earSpoken()),
		[]journal.Record{closed("idle")},
	))
	res := scan(t, store)
	if len(res.Turns) != 1 {
		t.Fatalf("turns = %+v, want the porch light alone", res.Turns)
	}
	tn := res.Turns[0]
	if tn.Said != "The porch light is on." {
		t.Errorf("said = %q, want the porch light without the apology", tn.Said)
	}
	if len(tn.Audio) != 1 || tn.Audio[0] != "blob://tts/s1" {
		t.Errorf("audio = %v, want the porch light's alone", tn.Audio)
	}
	for _, c := range tn.Calls {
		if c.ID == "cn_ear1" {
			t.Errorf("the ear's speak call is in the turn: %+v", tn.Calls)
		}
	}
}

// Alice cuts the zeppelin answer and the ear fails on her correction, so the
// kitchen apologises before she says it again. The pair is still the answer
// she cut and the answer to what she said; the apology is on neither side.
//
// verifies SPEC §9.1
func TestTheEarsApologyBetweenACutAndItsCorrectionIsOnNeitherSide(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{opened("kitchen")}, cutTurn(), earLine(earSpoken()), correctedTurn(),
		[]journal.Record{closed("model_ended")},
	))
	p := onePair(t, scan(t, store))
	if p.Rejected != "I found three" || p.RejectedUnheard != " albums by that artist" {
		t.Errorf("rejected = %q | %q, want the answer she cut alone", p.Rejected, p.RejectedUnheard)
	}
	if strings.Contains(p.AsSaid, "hearing") {
		t.Errorf("as said = %q, want the answer to her correction alone", p.AsSaid)
	}
	if len(p.Audio.Rejected) != 1 || p.Audio.Rejected[0] != "blob://tts/s1" {
		t.Errorf("rejected audio = %v, want the cut answer's alone", p.Audio.Rejected)
	}
	for _, c := range p.Calls {
		if c.ID == "cn_ear1" {
			t.Errorf("the ear's speak call is in the pair: %+v", p.Calls)
		}
	}
}

// Bob talks over the ear's apology. Cutting words no model chose is no
// preference, and it does not make the turn before it a cut one.
//
// verifies SPEC §9.1
func TestCuttingTheEarsApologyIsNoPreferencePair(t *testing.T) {
	store := conversation(t, versions(), concat(
		[]journal.Record{
			opened("kitchen"),
			heard("what's the weather tomorrow", "blob://mic/1"),
			speak("s1"),
			spoken("Sunny, with a high of nineteen.", "blob://tts/s1"),
			record(journal.KindToolResult, "", "call_id", "s1", "outcome", "ok"),
			completed(),
		},
		earLine(
			bargeIn("380", "blob://mic/3"),
			record(journal.KindSpeechTruncated, "blob://tts/cn_ear1", "spoken_text", "Sorry, ",
				"unspoken_text", "my hearing isn't working right now. Try again in a minute.",
				"frames_played", "6080", "call_id", "cn_ear1", "reason", "barge_in"),
		),
		correctedTurn(), []journal.Record{closed("model_ended")},
	))
	res := scan(t, store)
	if len(res.Pairs) != 0 || res.Uncorrected != 0 {
		t.Errorf("pairs = %+v uncorrected = %d, want neither: no model's words were cut", res.Pairs, res.Uncorrected)
	}
}
