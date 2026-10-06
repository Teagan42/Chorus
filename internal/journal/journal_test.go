package journal_test

import (
	"context"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
)

func versions() journal.Versions {
	return journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
}

// verifies SPEC §8
func TestAppendStampsMonotonicSequenceAndWallClock(t *testing.T) {
	clk := journal.FixedClock(time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC))
	j := journal.New(journal.NewMemStore(), clk, versions())

	for range 3 {
		if _, err := j.Append(context.Background(), "conv-1", journal.Record{
			Kind:   journal.KindSessionOpened,
			Fields: map[string]string{"satellite": "kitchen"},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	events, err := j.Events(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	for i, e := range events {
		if want := uint64(i + 1); e.Seq != want {
			t.Errorf("event %d: seq = %d, want %d", i, e.Seq, want)
		}
		if !e.At.Equal(clk.Now()) {
			t.Errorf("event %d: at = %v, want %v", i, e.At, clk.Now())
		}
	}
}

// verifies SPEC §8
func TestAppendRejectsMalformedWrites(t *testing.T) {
	cases := map[string]struct {
		versions journal.Versions
		record   journal.Record
	}{
		"undeclared kind": {versions(), journal.Record{Kind: "speech_maybe_spoken"}},
		"missing required field": {versions(), journal.Record{
			Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "c1"},
		}},
		"audio without a blob reference": {versions(), journal.Record{
			Kind: journal.KindUtteranceTranscribed, Fields: map[string]string{"text": "hello"},
		}},
		"versions required but incomplete": {journal.Versions{Model: "qwen3-32b@1"}, journal.Record{
			Kind: journal.KindWakeRejected, AudioRef: "blob/1", Fields: map[string]string{"reason": "no_speech"},
		}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), tc.versions)

			if _, err := j.Append(context.Background(), "conv-1", tc.record); err == nil {
				t.Fatalf("append accepted %s", name)
			}
		})
	}
}

// verifies SPEC §8
func TestAppendStampsTheVersionsInEffect(t *testing.T) {
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), versions())

	e, err := j.Append(context.Background(), "conv-1", journal.Record{
		Kind: journal.KindSpeechTruncated, AudioRef: "blob/1",
		Fields: map[string]string{"spoken_text": "I found", "unspoken_text": " three", "frames_played": "6720"},
	})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if e.Versions != versions() {
		t.Errorf("versions = %+v, want %+v", e.Versions, versions())
	}
}
