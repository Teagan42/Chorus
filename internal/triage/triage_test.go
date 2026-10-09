package triage_test

import (
	"context"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/triage"
)

func record(kind journal.Kind, audio string, fields ...string) journal.Record {
	r := journal.Record{Kind: kind, AudioRef: audio, Fields: map[string]string{}}
	for i := 0; i+1 < len(fields); i += 2 {
		r.Fields[fields[i]] = fields[i+1]
	}
	return r
}

func opened(satellite, speaker string) journal.Record {
	return record(journal.KindSessionOpened, "", "satellite", satellite, "speaker_id", speaker, "resumed", "false")
}

func heard(text, speaker string) journal.Record {
	return record(journal.KindUtteranceTranscribed, "blob://mic/x", "text", text, "speaker_id", speaker)
}

func called(tool, id string) journal.Record {
	return record(journal.KindToolCalled, "", "tool", tool, "call_id", id, "args_json", "{}")
}

func result(id, outcome string) journal.Record {
	return record(journal.KindToolResult, "", "call_id", id, "outcome", outcome)
}

func completed(reason string) journal.Record {
	return record(journal.KindModelCompleted, "", "completion_json", "{}", "finish_reason", reason)
}

func closed(reason string) journal.Record {
	return record(journal.KindSessionClosed, "", "reason", reason, "satellite", "kitchen")
}

func scan(t *testing.T, records ...journal.Record) []triage.Signal {
	t.Helper()
	store := journal.NewMemStore()
	v := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
	j := journal.New(store, journal.FixedClock(time.Unix(1_760_000_000, 0)), v)
	for _, r := range records {
		if _, err := j.Append(context.Background(), "conv-1", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	sigs, err := triage.Scan(context.Background(), store, "conv-1")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return sigs
}

func one(t *testing.T, sigs []triage.Signal) triage.Signal {
	t.Helper()
	if len(sigs) != 1 {
		t.Fatalf("got %d signals, want 1: %+v", len(sigs), sigs)
	}
	return sigs[0]
}

// A clean conversation is not triage's business.
//
// verifies SPEC §9.1
func TestACleanConversationRaisesNothing(t *testing.T) {
	sigs := scan(t,
		opened("kitchen", "alice"),
		heard("turn on the lights", "alice"),
		called("light.turn_on", "c1"), result("c1", "ok"),
		completed("stop"), closed("model_ended"),
	)
	if len(sigs) != 0 {
		t.Errorf("clean conversation raised %+v", sigs)
	}
}

// A tool that failed or timed out is a result the model reasoned about
// (SPEC §7); the turn it happened in is worth a listen.
//
// verifies SPEC §9.1
func TestAFailedToolIsAFailureOnTheTurnThatCalledIt(t *testing.T) {
	s := one(t, scan(t,
		opened("office", "teagan"),
		heard("is the garage door closed", "teagan"),
		called("cover.state", "c1"), result("c1", "timed_out"),
		completed("stop"),
	))
	if s.Kind != triage.KindFailure {
		t.Fatalf("kind = %s, want failure", s.Kind)
	}
	if s.Utterance != "is the garage door closed" || s.Speaker != "teagan" || s.Satellite != "office" {
		t.Errorf("context = %q / %q / %q", s.Utterance, s.Speaker, s.Satellite)
	}
	if s.Detail != "cover.state timed_out" {
		t.Errorf("detail = %q", s.Detail)
	}
}

// verifies SPEC §9.1
func TestAModelOrSessionErrorIsAFailure(t *testing.T) {
	sigs := scan(t,
		opened("kitchen", "alice"),
		heard("what's on tomorrow", "alice"),
		completed("error"),
		closed("device_lost"),
	)
	if len(sigs) != 2 {
		t.Fatalf("got %d signals, want the model error and the lost device: %+v", len(sigs), sigs)
	}
	for _, s := range sigs {
		if s.Kind != triage.KindFailure || s.Utterance != "what's on tomorrow" {
			t.Errorf("signal = %+v", s)
		}
	}
	if sigs[0].Detail != "model finished with error" || sigs[1].Detail != "session closed: device_lost" {
		t.Errorf("details = %q, %q", sigs[0].Detail, sigs[1].Detail)
	}
}

// A cancelled or detached tool is the barge-in or the model working as
// designed, not a failure.
//
// verifies SPEC §9.1
func TestCancelledAndDetachedToolsAreNotFailures(t *testing.T) {
	sigs := scan(t,
		opened("kitchen", "alice"),
		heard("play something", "alice"),
		called("speak", "s1"), result("s1", "cancelled"),
		called("timer.start", "t1"), result("t1", "detached"),
		completed("stop"),
	)
	if len(sigs) != 0 {
		t.Errorf("raised %+v", sigs)
	}
}

// A different voice mid-conversation means person-scoped tools may have
// answered the wrong person (SPEC §5).
//
// verifies SPEC §9.1
func TestASpeakerChangeMidConversationIsAFlip(t *testing.T) {
	s := one(t, scan(t,
		opened("kitchen", "teagan"),
		heard("add oat milk to the list", "teagan"),
		completed("stop"),
		heard("and eggs", "guest"),
		completed("stop"),
	))
	if s.Kind != triage.KindSpeakerFlip || s.Detail != "teagan → guest" || s.Utterance != "and eggs" {
		t.Errorf("signal = %+v", s)
	}
}

// verifies SPEC §9.1
func TestABargeInIsSignalledWithItsPair(t *testing.T) {
	s := one(t, scan(t,
		opened("kitchen", "alice"),
		heard("play something by zeppelin", "alice"),
		called("speak", "s1"),
		record(journal.KindBargeInDetected, "blob://mic/2", "tts_position_ms", "420"),
		record(journal.KindSpeechTruncated, "blob://tts/s1", "spoken_text", "I found three", "unspoken_text", " albums", "frames_played", "2080"),
		result("s1", "cancelled"),
		completed("stop"),
		heard("just the first one", "alice"),
		completed("stop"),
	))
	if s.Kind != triage.KindBargeIn || s.PairID != "conv-1/5" {
		t.Fatalf("signal = %+v, want a barge-in naming its pair", s)
	}
	if s.Utterance != "play something by zeppelin" || s.Detail != "cut → “just the first one”" {
		t.Errorf("utterance %q detail %q", s.Utterance, s.Detail)
	}
}

// A detach-policy tool outlives the barge-in that ended its turn, so its
// failure can land after the correction. The row belongs to the turn that
// called it, not to whoever spoke last.
//
// verifies SPEC §9.1
func TestALateToolFailureKeepsTheTurnThatCalledIt(t *testing.T) {
	s := one(t, scan(t,
		opened("kitchen", "alice"),
		heard("download the new album", "alice"),
		called("media.fetch", "c1"),
		completed("stop"),
		record(journal.KindSessionClosed, "", "reason", "migrated", "satellite", "kitchen"),
		opened("office", "alice"),
		heard("never mind", "alice"),
		result("c1", "error"),
		completed("stop"),
	))
	if s.Utterance != "download the new album" || s.Satellite != "kitchen" {
		t.Errorf("late failure filed under %q on %s, want the calling turn in the kitchen", s.Utterance, s.Satellite)
	}
	if s.Session != 1 {
		t.Errorf("late failure filed under session #%d, want the kitchen's opening at #1", s.Session)
	}
}

// wallClock is set before each append, so a fixture says when each thing
// was heard; a repeat is a question about seconds.
type wallClock struct{ now time.Time }

func (c *wallClock) Now() time.Time { return c.now }

// timed is one record and how many seconds into the conversation it landed.
type timed struct {
	sec float64
	rec journal.Record
}

func scanTimed(t *testing.T, recs ...timed) []triage.Signal {
	t.Helper()
	store := journal.NewMemStore()
	clk := &wallClock{}
	start := time.Unix(1_760_000_000, 0)
	j := journal.New(store, clk, journal.Versions{Model: "qwen3:32b", Prompt: "sys@3", ToolSchema: "tools@7"})
	for _, r := range recs {
		clk.now = start.Add(time.Duration(r.sec * float64(time.Second)))
		if _, err := j.Append(context.Background(), "conv-1", r.rec); err != nil {
			t.Fatalf("append %s: %v", r.rec.Kind, err)
		}
	}
	sigs, err := triage.Scan(context.Background(), store, "conv-1")
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return sigs
}

func repeats(sigs []triage.Signal) []triage.Signal {
	var out []triage.Signal
	for _, s := range sigs {
		if s.Kind == triage.KindRepeated {
			out = append(out, s)
		}
	}
	return out
}

func spoken(text string) journal.Record {
	r := record(journal.KindSpeechSpoken, "blob://tts/x", "text", text, "frames_played", "16000")
	return r
}

// Alan asks for an oven timer, the assistant asks how long instead of
// setting one, and he says it again with the duration. SPEC §9.1: a
// repeated request is a failure.
//
// verifies SPEC §9.1
func TestAskingAgainSoonAfterIsARepeat(t *testing.T) {
	sigs := repeats(scanTimed(t,
		timed{0, opened("kitchen", "alan")},
		timed{0.2, heard("set a timer for the oven", "alan")},
		timed{2.6, called("speak", "s1")},
		timed{4.4, spoken("Sure, how long?")},
		timed{4.5, completed("stop")},
		timed{6.4, heard("set a timer for twelve minutes", "alan")},
		timed{6.9, called("ha_call_service", "c1")}, // timer.start on the oven timer
	))
	s := one(t, sigs)
	if s.Seq != 6 || s.Utterance != "set a timer for twelve minutes" || s.Speaker != "alan" || s.Satellite != "kitchen" {
		t.Errorf("repeat = %+v, want the second ask in the kitchen", s)
	}
	if s.Detail != "asked again 6.2 s after “set a timer for the oven” · no tool call on the first ask" {
		t.Errorf("detail = %q", s.Detail)
	}
}

// verifies SPEC §9.1
func TestAFollowUpIsNotARepeat(t *testing.T) {
	sigs := scanTimed(t,
		timed{0, opened("kitchen", "teagan")},
		timed{0.3, heard("turn off the kitchen lights", "teagan")},
		timed{1.1, called("ha_call_service", "c1")},
		timed{4.0, heard("and the porch light", "teagan")},
		timed{9.0, heard("what's the weather tomorrow", "teagan")},
	)
	if r := repeats(sigs); len(r) != 0 {
		t.Errorf("follow-ups raised %+v", r)
	}
}

// verifies SPEC §9.1
func TestTheSameAskMinutesLaterIsNotARepeat(t *testing.T) {
	sigs := scanTimed(t,
		timed{0, opened("office", "teagan")},
		timed{0.4, heard("is the garage door closed", "teagan")},
		timed{1.0, called("ha_get_state", "c1")},
		timed{45, heard("is the garage door closed", "teagan")},
	)
	if r := repeats(sigs); len(r) != 0 {
		t.Errorf("an ask 45 s later raised %+v", r)
	}
}

// verifies SPEC §9.1
func TestSomeoneElseAskingTheSameIsNotARepeat(t *testing.T) {
	sigs := scanTimed(t,
		timed{0, opened("living_room", "teagan")},
		timed{0.4, heard("is the garage door closed", "teagan")},
		timed{5.0, heard("is the garage door closed", "alan")},
	)
	if r := repeats(sigs); len(r) != 0 {
		t.Errorf("Alan asking what Teagan asked raised %+v", r)
	}
}

// verifies SPEC §9.1
func TestShortAnswersAreNotRepeats(t *testing.T) {
	sigs := scanTimed(t,
		timed{0, opened("kitchen", "alice")},
		timed{0.3, heard("play something by zeppelin", "alice")},
		timed{3.0, heard("yes", "alice")},
		timed{6.0, heard("yes please", "alice")},
	)
	if r := repeats(sigs); len(r) != 0 {
		t.Errorf("confirming twice raised %+v", r)
	}
}

// "Yes" is an answer, not the request it answers: Teagan confirms, nothing
// happens, and she asks again.
//
// verifies SPEC §9.1
func TestAConfirmationInBetweenDoesNotHideARepeat(t *testing.T) {
	sigs := repeats(scanTimed(t,
		timed{0, opened("kitchen", "teagan")},
		timed{0.3, heard("turn off the kitchen lights", "teagan")},
		timed{2.0, called("speak", "s1")},
		timed{3.1, spoken("All of them?")},
		timed{4.5, heard("yes", "teagan")},
		timed{12.3, heard("turn off the kitchen lights", "teagan")},
	))
	s := one(t, sigs)
	if s.Seq != 6 || s.Detail != "asked again 12.0 s after “turn off the kitchen lights” · no tool call on the first ask" {
		t.Errorf("repeat = #%d %q", s.Seq, s.Detail)
	}
}

// An unidentified voice after an identified one is a voice speaker ID could
// not place, perhaps a guest: not evidence it is the same person.
//
// verifies SPEC §5
func TestAnUnknownVoiceAfterAKnownOneIsNotARepeat(t *testing.T) {
	sigs := scanTimed(t,
		timed{0, opened("living_room", "teagan")},
		timed{0.4, heard("is the garage door closed", "teagan")},
		timed{5.0, heard("is the garage door closed", "")},
	)
	if r := repeats(sigs); len(r) != 0 {
		t.Errorf("an unplaced voice asking what Teagan asked raised %+v", r)
	}
}

// Without the speaker sidecar every utterance is unidentified (ADR-0031).
// Requiring a known speaker would switch the signal off for that household;
// one wake on one satellite is one person far more often than two.
//
// verifies SPEC §9.1
func TestWithoutSpeakerIDARepeatStillCounts(t *testing.T) {
	sigs := repeats(scanTimed(t,
		timed{0, opened("kitchen", "")},
		timed{0.2, heard("set a timer for the oven", "")},
		timed{6.4, heard("set a timer for twelve minutes", "")},
	))
	if s := one(t, sigs); s.Satellite != "kitchen" {
		t.Errorf("repeat = %+v", s)
	}
}
