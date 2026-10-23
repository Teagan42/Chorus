package listen_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
)

// Alan says "turn on the porch light" in a voice Smart Turn is unsure of:
// 0.704, above the old 0.5 cut and below the 0.8 band. Unsure is not
// finished, so the turn does not end at the pause; it ends where Energy
// would, 800 ms after the last word, as if nobody had judged it (ADR-0094).
//
// verifies SPEC §4.5
func TestSemanticEndsATurnTheJudgeIsUnsureOfWhereEnergyWould(t *testing.T) {
	j := &judge{answers: []judgement{{p: torn}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(1500))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	got := feed(t, ep, 0, listen.DefaultSilence)
	if want := listen.DefaultSilence - pauseChunks*chunkBytes; got != want {
		t.Fatalf("an unsure turn ended %d bytes after its pause was judged, want at the silence (%d)", got, want)
	}
	if e := ep.Ending(); e.Rule != listen.EndUnsure || e.Probability != torn {
		t.Errorf("Ending = %+v, want unsure at %v", e, torn)
	}
}

// "Set a timer for", said on a falling pitch: Smart Turn is sure at 0.988 and
// the words still dangle, so the turn is held to the hold, and it says it was
// the words that held it.
//
// verifies SPEC §4.5
func TestSemanticHoldsDanglingWordsWhateverTheProbability(t *testing.T) {
	j := &judge{answers: []judgement{{p: sure, dangling: true}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(1200))
	feed(t, ep, 0, pauseChunks*chunkBytes)
	q.run()
	if feed(t, ep, 0, listen.DefaultSilence) != -1 {
		t.Fatal("a turn ending on \"for\" ended at Energy's silence")
	}
	if feed(t, ep, 0, listen.DefaultHold) == -1 {
		t.Fatal("a dangling turn was held open past the hold")
	}
	if e := ep.Ending(); e.Rule != listen.EndDangling || e.Probability != sure {
		t.Errorf("Ending = %+v, want dangling at Smart Turn's %v", e, sure)
	}
}

// Every rule the endpointer can end a turn on, named with the probability it
// ended on, so the journal can say which one the household waited through.
//
// verifies SPEC §4.5
func TestSemanticSaysHowEachTurnEnded(t *testing.T) {
	down := errors.New("smartturn judge: dial tcp 127.0.0.1:8891: connection refused")
	for _, tc := range []struct {
		name   string
		answer judgement
		quiet  int
		want   listen.Ending
	}{
		{"kitchen lights, sure", judgement{p: sure}, chunkBytes, listen.Ending{Rule: listen.EndFinished, Probability: sure}},
		{"porch light, unsure", judgement{p: torn}, listen.DefaultSilence, listen.Ending{Rule: listen.EndUnsure, Probability: torn}},
		{"turn off the uh, doubted", judgement{p: doubtful}, listen.DefaultHold, listen.Ending{Rule: listen.EndHeld, Probability: doubtful}},
		{"set a timer for, dangling", judgement{p: sure, dangling: true}, listen.DefaultHold, listen.Ending{Rule: listen.EndDangling, Probability: sure}},
		{"sidecar down", judgement{err: down}, listen.DefaultSilence, listen.Ending{Rule: listen.EndSilence}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ep, q := semantic(&judge{answers: []judgement{tc.answer}}, nil)
			speakTo(t, ep, 8000, ms(1500))
			feed(t, ep, 0, pauseChunks*chunkBytes)
			q.run()
			if feed(t, ep, 0, tc.quiet) == -1 {
				t.Fatalf("no End within %d bytes of quiet", tc.quiet)
			}
			if got := ep.Ending(); got != tc.want {
				t.Errorf("Ending = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A judge slower than the silence decided nothing: the turn ended on Energy's
// rule, and the late verdict does not rename it.
//
// verifies SPEC §4.5
func TestSemanticSaysASlowJudgeDecidedNothing(t *testing.T) {
	j := &judge{answers: []judgement{{p: sure}}}
	ep, q := semantic(j, nil)

	speakTo(t, ep, 8000, ms(1900))
	if feed(t, ep, 0, listen.DefaultSilence) == -1 {
		t.Fatal("no End at the silence")
	}
	q.run()
	if e := ep.Ending(); e.Rule != listen.EndSilence {
		t.Errorf("Ending = %+v, want silence", e)
	}
}

// Sure and Doubt equal are one cut, the one chorusd had before the bands:
// the porch light at 0.704 ends at the pause, and "what's the weather on"
// at 0.412 is held, words or no words.
//
// verifies SPEC §4.5
func TestSemanticBandsEqualAreASingleCut(t *testing.T) {
	const weatherOn = 0.412
	for _, tc := range []struct {
		p     float64
		quiet int
		rule  listen.EndRule
	}{
		{torn, chunkBytes, listen.EndFinished},
		{weatherOn, listen.DefaultHold, listen.EndHeld},
	} {
		ep, q := semantic(&judge{answers: []judgement{{p: tc.p}}}, nil)
		ep.Sure, ep.Doubt = 0.5, 0.5
		speakTo(t, ep, 8000, ms(1300))
		feed(t, ep, 0, pauseChunks*chunkBytes)
		q.run()
		got := feed(t, ep, 0, tc.quiet)
		if got == -1 || (tc.rule == listen.EndFinished && got != chunkBytes) {
			t.Errorf("p=%v ended %d bytes after its verdict", tc.p, got)
		}
		if e := ep.Ending(); e.Rule != tc.rule {
			t.Errorf("p=%v ended as %s, want %s", tc.p, e.Rule, tc.rule)
		}
	}
}

// "Set a timer for" in a voice Smart Turn is unsure of, 0.549: unsure would
// end the turn at the silence, so the words are read for it too, and they
// hold it.
//
// verifies SPEC §4.5
func TestDanglingReadsTheWordsOfAnUnsureVerdict(t *testing.T) {
	const timerFor = 0.549
	j := &judge{answers: []judgement{{p: timerFor}}}
	w := newWords("Set a timer for")

	v, err := listen.Dangling{Judge: j, Words: w}.Complete(context.Background(), voice(8000, ms(1300)))
	if err != nil || !v.Dangling || v.Probability != timerFor {
		t.Fatalf("Complete = %+v, %v; want %v, dangling", v, err, timerFor)
	}
}

// Under the household's Doubt the turn is held whatever the words say, so a
// Dangling built with the endpointer's own band does not wait for a decode
// the verdict cannot use.
//
// verifies SPEC §4.5
func TestDanglingDoesNotWaitForWordsUnderItsDoubt(t *testing.T) {
	const timerFor = 0.549
	j := &judge{answers: []judgement{{p: timerFor}}}
	w := newWords("Set a timer for")
	w.release = make(chan struct{})

	v, err := listen.Dangling{Judge: j, Words: w, Doubt: 0.6}.Complete(context.Background(), voice(8000, ms(1300)))
	if err != nil || v.Dangling || v.Probability != timerFor {
		t.Fatalf("Complete = %+v, %v; want the judge's %v as it was", v, err, timerFor)
	}
	if !w.done() {
		t.Error("the decode outlived the ask it was for")
	}
}

// Through the whole listener: the kitchen satellite's journal says the
// porch-light turn ended because Smart Turn was unsure, and at what, so the
// household can tune the bands from its own voices (ADR-0094).
//
// verifies SPEC §4.5, §8
func TestAnUnsureTurnIsJournalledWithItsProbability(t *testing.T) {
	j := &judge{answers: []judgement{{p: torn}}}
	ep, q := semantic(j, nil)
	ep.Threshold = 0.05
	r := newRig(t, talking(), func(c *listen.Config) {
		c.Clock = journal.FixedClock(time.Date(2026, 10, 23, 21, 4, 0, 0, time.UTC))
		c.Endpointer = ep
	})
	r.speaker.dac = true
	porch := r.line("turn on the porch light", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.speak(t, porch, 8*chunkBytes)
	r.pause(t, pauseChunks*chunkBytes)
	r.settled(t)
	q.run()
	r.pause(t, listen.DefaultSilence)

	conv := r.session(t).ConversationID()
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	if heard.Fields["turn_end"] != "unsure" || heard.Fields["turn_probability"] != "0.704" {
		t.Errorf("utterance_transcribed = %v, want turn_end unsure at 0.704", heard.Fields)
	}
}

// Energy has no judge and says nothing about how a turn ended: the field is
// left out rather than claiming a rule nobody applied.
//
// verifies SPEC §8
func TestAnEnergyTurnJournalsNoTurnEnd(t *testing.T) {
	r := newRig(t, talking())
	r.speaker.dac = true
	lights := r.line("turn off the kitchen lights", alan)

	r.dev.SendWake(t, "hey_eddie")
	r.utter(t, lights, 8*chunkBytes)

	conv := r.session(t).ConversationID()
	heard := r.awaitKind(t, conv, journal.KindUtteranceTranscribed, 1)
	for _, f := range []string{"turn_end", "turn_probability"} {
		if v, ok := heard.Fields[f]; ok {
			t.Errorf("%s = %q under Energy", f, v)
		}
	}
}
