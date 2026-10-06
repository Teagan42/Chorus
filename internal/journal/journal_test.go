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
func TestAppendRejectsUnknownKind(t *testing.T) {
	j := journal.New(journal.NewMemStore(), journal.FixedClock(time.Unix(0, 0)), versions())

	_, err := j.Append(context.Background(), "conv-1", journal.Record{Kind: "speech_maybe_spoken"})
	if err == nil {
		t.Fatal("append accepted an undeclared kind")
	}
}
