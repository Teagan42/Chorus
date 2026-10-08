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
