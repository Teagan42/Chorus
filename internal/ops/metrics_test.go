package ops_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/ops"
)

var _ journal.Observer = (*ops.Metrics)(nil)

// refusingStore fails every write, as a Postgres that went away does.
type refusingStore struct{ journal.Store }

func (refusingStore) Append(context.Context, journal.Event) error {
	return errors.New("connection refused")
}

// thursday is a morning's log: teagan in the kitchen, alice interrupting
// from the office, one slow tool, one dead model and one dead voice.
func thursday(t *testing.T, j *journal.Journal) {
	t.Helper()
	type rec struct {
		conv string
		kind journal.Kind
		kv   []string
	}
	for _, r := range []rec{
		{"conv-teagan", journal.KindSessionOpened, []string{"satellite", "kitchen", "speaker_id", "teagan"}},
		{"conv-teagan", journal.KindToolCalled, []string{"tool", "ha_call_service", "call_id", "c1", "args_json", `{"domain":"light"}`}},
		{"conv-teagan", journal.KindToolResult, []string{"call_id", "c1", "outcome", "ok"}},
		{"conv-teagan", journal.KindToolCalled, []string{"tool", "ha_get_state", "call_id", "c2", "args_json", `{"entity_id":"cover.garage_door"}`}},
		{"conv-teagan", journal.KindToolResult, []string{"call_id", "c2", "outcome", "timed_out"}},
		{"conv-teagan", journal.KindToolResult, []string{"call_id", "c2", "outcome", "detached"}},
		{"conv-teagan", journal.KindSpeechStarted, []string{"call_id", "s1", "wait_ms", "640"}},
		{"conv-teagan", journal.KindSessionClosed, []string{"reason", "model_ended", "satellite", "kitchen"}},
		{"conv-alice", journal.KindSessionOpened, []string{"satellite", "office", "speaker_id", "alice"}},
		{"conv-alice", journal.KindToolCalled, []string{"tool", "speak", "call_id", "c1", "args_json", `{"text":"It's raining."}`}},
		{"conv-alice", journal.KindBargeInRejected, []string{"stage", "speaker_id"}},
		{"conv-alice", journal.KindBargeInDetected, []string{"tts_position_ms", "1200"}},
		{"conv-alice", journal.KindToolResult, []string{"call_id", "c1", "outcome", "cancelled"}},
		{"conv-alice", journal.KindSpeechStarted, []string{"call_id", "s2", "wait_ms", "2400"}},
		{"conv-alice", journal.KindModelFailed, []string{"reason", "timed_out", "error", "no output for 90s"}},
		{"conv-alice", journal.KindSpeechFailed, []string{"call_id", "s3", "reason", "tts_unavailable"}},
		{"conv-alice", journal.KindToolResult, []string{"call_id", "c9", "outcome", "error"}},
		{"conv-alice", journal.KindAudioDropped, []string{"audio_ref", "blob://mic/office-1", "reason", "disk_low"}},
		{"conv-alice", journal.KindSessionClosed, []string{"reason", "device_lost", "satellite", "office"}},
	} {
		fields := map[string]string{}
		for i := 0; i+1 < len(r.kv); i += 2 {
			fields[r.kv[i]] = r.kv[i+1]
		}
		ref := ""
		if journal.Meta[r.kind].HasAudio {
			ref = "blob://mic/" + r.conv
		}
		if _, err := j.Append(context.Background(), r.conv, journal.Record{Kind: r.kind, Fields: fields, AudioRef: ref}); err != nil {
			t.Fatalf("append %s: %v", r.kind, err)
		}
	}
}

func versions() journal.Versions {
	return journal.Versions{Model: "qwen3-32b", Prompt: "sys@3", ToolSchema: "tools@7"}
}

// Every counter is derived from the journal as it is written: the observer
// the append path calls counts what each event records, so the metrics and
// the log cannot disagree about what happened (SPEC §8, ADR-0007).
//
// verifies SPEC §8, §7, §4.3
func TestMetricsCountWhatTheJournalRecords(t *testing.T) {
	hs := newHouse(t)
	j := journal.New(journal.NewMemStore(), hs.clk, versions())
	j.Observe(hs.ops.Metrics())
	thursday(t, j)

	for series, want := range map[string]float64{
		`chorus_sessions_opened_total`:                                          2,
		`chorus_sessions_closed_total{reason="model_ended"}`:                    1,
		`chorus_sessions_closed_total{reason="device_lost"}`:                    1,
		`chorus_barge_ins_detected_total`:                                       1,
		`chorus_barge_ins_rejected_total{stage="speaker_id"}`:                   1,
		`chorus_model_failures_total{reason="timed_out"}`:                       1,
		`chorus_speech_failures_total{reason="tts_unavailable"}`:                1,
		`chorus_tool_calls_total{outcome="ok",tool="ha_call_service"}`:          1,
		`chorus_tool_calls_total{outcome="timed_out",tool="ha_get_state"}`:      1,
		`chorus_tool_calls_total{outcome="detached",tool="ha_get_state"}`:       1,
		`chorus_tool_calls_total{outcome="cancelled",tool="speak"}`:             1,
		`chorus_tool_calls_total{outcome="error",tool="unknown"}`:               1,
		`chorus_audio_dropped_total{reason="disk_low"}`:                         1,
		`chorus_first_audio_seconds_count`:                                      2,
		`chorus_first_audio_seconds_bucket{le="0.7"}`:                           1,
		`chorus_first_audio_seconds_bucket{le="3"}`:                             2,
		`chorus_journal_append_errors_total{kind="session_opened"}`:             -1,
		`chorus_build_info{version="0.17.0"}`:                                   1,
		`chorus_tool_calls_total{outcome="ok",tool="speak"}`:                    -1,
		`chorus_sessions_closed_total{reason="error"}`:                          -1,
		`chorus_tool_calls_total{outcome="confirmation_required",tool="speak"}`: -1,
	} {
		if got := sample(t, hs.h, series); got != want {
			t.Errorf("%s = %v, want %v", series, got, want)
		}
	}
	if got := sample(t, hs.h, "chorus_first_audio_seconds_sum"); got < 3.039 || got > 3.041 {
		t.Errorf("chorus_first_audio_seconds_sum = %v, want the two waits, 3.04 s", got)
	}
}

// A write the journal could not make is counted by the kind it would have
// been, since the log itself cannot say it is missing (SPEC §8).
//
// verifies SPEC §8
func TestAJournalWriteThatFailsIsCounted(t *testing.T) {
	hs := newHouse(t)
	j := journal.New(refusingStore{journal.NewMemStore()}, hs.clk, versions())
	j.Observe(hs.ops.Metrics())
	_, err := j.Append(context.Background(), "conv-teagan", journal.Record{
		Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen"},
	})
	if err == nil {
		t.Fatal("the refusing store accepted a write")
	}
	if got := sample(t, hs.h, `chorus_journal_append_errors_total{kind="session_opened"}`); got != 1 {
		t.Errorf("append errors = %v, want 1", got)
	}
	if got := sample(t, hs.h, "chorus_sessions_opened_total"); got > 0 {
		t.Errorf("a session that never reached the log was counted as opened")
	}
}

// Audio bytes are counted as they are committed, never for a blob aborted
// part-way; a write the disk guard declined is counted on its own, since it
// wrote nothing (SPEC §8, ADR-0065).
//
// verifies SPEC §8
func TestBlobBytesAndDeclinedWritesAreCounted(t *testing.T) {
	hs := newHouse(t)
	m := hs.ops.Metrics()
	store := m.MeterBlobs(blob.NewMemory())

	kept, err := store.Create(context.Background(), "mic/kitchen-kettle")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _ = kept.Write(make([]byte, 1024))
	_, _ = kept.Write(make([]byte, 1024))
	if _, err := kept.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	dropped, err := store.Create(context.Background(), "mic/office-lights")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_, _ = dropped.Write(make([]byte, 4096))
	_ = dropped.Abort()
	m.BlobDeclined("blob://mic/office-later")

	if got := sample(t, hs.h, "chorus_blob_bytes_written_total"); got != 2048 {
		t.Errorf("blob bytes = %v, want the committed 2048", got)
	}
	if got := sample(t, hs.h, "chorus_blob_writes_declined_total"); got != 1 {
		t.Errorf("declined writes = %v, want 1", got)
	}
	if r, ok := store.(interface {
		Remove(context.Context, string) error
	}); !ok || r.Remove(context.Background(), "blob://mic/kitchen-kettle") != nil {
		t.Error("the metered store does not remove through to the one it wraps, as retention needs")
	}
	if !strings.Contains(get(t, hs.h, "/metrics").Body.String(), "# HELP chorus_blob_bytes_written_total") {
		t.Error("the scrape does not describe its metrics")
	}
}
