package triage_test

import (
	"context"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/triage"
)

// positives is ScanAll's weak positives over a log written the way scanTimed
// writes it.
func positives(t *testing.T, recs ...timed) []triage.Signal {
	t.Helper()
	store := journal.NewMemStore()
	clk := &wallClock{}
	start := time.Unix(1_760_000_000, 0)
	j := journal.New(store, clk, journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"})
	for _, r := range recs {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		if _, err := j.Append(context.Background(), "conv-1", r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	_, pos, err := triage.ScanAll(context.Background(), store, "conv-1")
	if err != nil {
		t.Fatalf("weak positives: %v", err)
	}
	return pos
}

// Teagan adds oat milk and walks off: nobody cut it off, asked again or saw
// a tool fail. SPEC §9.1: a completed turn with no correction is a weak
// positive, anchored where the model completed it.
//
// verifies SPEC §9.1
func TestATurnNobodyCorrectedIsAWeakPositive(t *testing.T) {
	recs := []timed{
		{0, opened("office", "teagan")},
		{0.3, heard("add oat milk to the shopping list", "teagan")},
		{1.2, called("ha_call_service", "c1")},
		{1.5, result("c1", "ok")},
		{1.7, called("speak", "s1")},
		{2.7, spoken("Added oat milk.")},
		{2.8, result("s1", "ok")},
		{2.9, completed("stop")},
		{8, closed("model_ended")},
	}
	s := one(t, positives(t, recs...))
	if s.Kind != triage.KindWeakPositive || s.Seq != 8 {
		t.Errorf("signal = %s at #%d, want a weak positive at the completion #8", s.Kind, s.Seq)
	}
	if s.Utterance != "add oat milk to the shopping list" || s.Speaker != "teagan" || s.Satellite != "office" {
		t.Errorf("context = %q / %q / %q", s.Utterance, s.Speaker, s.Satellite)
	}
	if s.Detail != "answered “Added oat milk.” · not cut off, asked again or failed" {
		t.Errorf("detail = %q", s.Detail)
	}
	if got := scanTimed(t, recs...); len(got) != 0 {
		t.Errorf("a weak positive is not a problem, but Scan raised %+v", got)
	}
}

// The oven timer goes off while Teagan is adding oat milk, and the kitchen
// says so behind the answer. Those are the house's words, not the turn's
// (SPEC §4): the weak positive stands, saying only what the turn said.
//
// verifies SPEC §9.1
func TestAnAnnouncementDuringATurnIsNotItsAnswer(t *testing.T) {
	recs := []timed{
		{0, opened("kitchen", "teagan")},
		{0.3, heard("add oat milk to the shopping list", "teagan")},
		{1.2, called("ha_call_service", "c1")},
		{1.5, result("c1", "ok")},
		{1.7, called("speak", "s1")},
		{1.8, record(journal.KindAnnouncementMade, "", "text", "The oven timer is done.", "call_id", "an_1", "source", "timer", "timer_id", "t1")},
		{1.8, called("speak", "an_1")},
		{2.7, spokenBy("s1", "Added oat milk.")},
		{2.8, result("s1", "ok")},
		{2.9, completed("stop")},
		{4.3, spokenBy("an_1", "The oven timer is done.")},
		{4.4, result("an_1", "ok")},
		{8, closed("model_ended")},
	}
	s := one(t, positives(t, recs...))
	if s.Kind != triage.KindWeakPositive || s.Seq != 10 || s.Utterance != "add oat milk to the shopping list" {
		t.Errorf("signal = %+v, want the oat milk turn's completion #10", s)
	}
	if s.Detail != "answered “Added oat milk.” · not cut off, asked again or failed" {
		t.Errorf("detail = %q, want only the turn's own words", s.Detail)
	}
	if got := scanTimed(t, recs...); len(got) != 0 {
		t.Errorf("an announcement is no problem, but Scan raised %+v", got)
	}
}

// Alice cuts the album list off; the answer to her correction stands. Only
// the turn nobody corrected is a positive.
//
// verifies SPEC §9.1
func TestACutTurnIsNoWeakPositiveButItsAnswerIs(t *testing.T) {
	sigs := positives(t,
		timed{0, opened("kitchen", "alice")},
		timed{0.3, heard("play something by zeppelin", "alice")},
		timed{2.3, called("speak", "s1")},
		timed{3.1, record(journal.KindBargeInDetected, "blob://mic/2", "tts_position_ms", "800")},
		timed{3.2, record(journal.KindSpeechTruncated, "blob://tts/s1", "spoken_text", "I found three", "unspoken_text", " albums", "frames_played", "12800")},
		timed{3.2, result("s1", "cancelled")},
		timed{3.3, completed("stop")},
		timed{4.1, heard("just the first one", "alice")},
		timed{5.6, called("speak", "s2")}, timed{7.1, spoken("Playing Led Zeppelin one.")},
		timed{7.3, completed("stop")},
	)
	if s := one(t, sigs); s.Utterance != "just the first one" {
		t.Errorf("positive = %+v, want only the answer to the correction", s)
	}
}

// Alan asks for the oven timer, is asked how long, and says it again: the
// first answer failed (SPEC §9.1), the second one set the timer.
//
// verifies SPEC §9.1
func TestARepeatedAskIsNoWeakPositive(t *testing.T) {
	sigs := positives(t,
		timed{0, opened("kitchen", "alan")},
		timed{0.2, heard("set a timer for the oven", "alan")},
		timed{2.6, called("speak", "s1")}, timed{4.4, spoken("Sure, how long?")},
		timed{4.6, completed("stop")},
		timed{6.4, heard("set a timer for twelve minutes", "alan")},
		timed{6.9, called("timer_start", "c1")}, timed{7.2, result("c1", "ok")},
		timed{9.2, completed("stop")},
	)
	if s := one(t, sigs); s.Utterance != "set a timer for twelve minutes" {
		t.Errorf("positive = %+v, want only the ask that set the timer", s)
	}
}

// A turn whose tool failed, whose model errored, or that never completed is
// no positive however quietly the person took it.
//
// verifies SPEC §9.1
func TestAFailedOrUnfinishedTurnIsNoWeakPositive(t *testing.T) {
	sigs := positives(t,
		timed{0, opened("office", "teagan")},
		timed{0.3, heard("is the garage door closed", "teagan")},
		timed{1.0, called("ha_get_state", "c1")}, timed{6.0, result("c1", "timed_out")},
		timed{8.4, completed("stop")},
		timed{40, heard("what's on the calendar tomorrow", "teagan")},
		timed{41, completed("error")},
		timed{80, heard("turn off the office lights", "teagan")},
		timed{81, called("ha_call_service", "c2")},
		timed{82, closed("device_lost")},
	)
	if len(sigs) != 0 {
		t.Errorf("raised %+v", sigs)
	}
}
