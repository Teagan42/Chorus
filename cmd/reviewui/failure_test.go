package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// The kitchen's evening with Ollama down and then Kokoro dying mid-forecast,
// as chorusd journals it (ADR-0051).
func failingKitchen(t *testing.T) *journal.MemStore {
	t.Helper()
	store := journal.NewMemStore()
	ctx := context.Background()
	v := journal.Versions{Model: "qwen3-32b@1", Prompt: "sys@3", ToolSchema: "tools@7"}
	at := thursday.Add(19 * time.Hour)
	for i, r := range []journal.Record{
		{Kind: journal.KindSessionOpened, Fields: map[string]string{"satellite": "kitchen", "speaker_id": "alan", "resumed": "false"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/lights", Fields: map[string]string{"text": "turn off the kitchen lights", "speaker_id": "alan"}},
		{Kind: journal.KindModelFailed, Fields: map[string]string{"reason": "unavailable", "error": "ollama chat: dial tcp 10.0.0.20:11434: connect: connection refused", "canned_call_id": "cn_77d0a1b2"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "cn_77d0a1b2", "args_json": `{"text":"Sorry, I can't think straight right now. Give me a minute and ask again.","mode":"queue","canned":true}`}},
		{Kind: journal.KindSpeechSpoken, AudioRef: "blob://tts/cn_77d0a1b2", Fields: map[string]string{"text": "Sorry, I can't think straight right now. Give me a minute and ask again.", "frames_played": "72000", "call_id": "cn_77d0a1b2"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "cn_77d0a1b2", "outcome": "ok"}},
		{Kind: journal.KindUtteranceTranscribed, AudioRef: "blob://mic/weather", Fields: map[string]string{"text": "what's the weather tomorrow", "speaker_id": "alan"}},
		{Kind: journal.KindToolCalled, Fields: map[string]string{"tool": "speak", "call_id": "call_w1", "args_json": `{"mode":"queue","streamed":true,"text":"Tomorrow will be sunny, with a high of nineteen."}`}},
		{Kind: journal.KindSpeechFailed, Fields: map[string]string{"call_id": "call_w1", "reason": "tts_unavailable", "error": `synthesize "with a high of nineteen.": kokoro: 503 Service Unavailable`, "canned_call_id": "cn_5e21c3d4"}},
		{Kind: journal.KindSpeechTruncated, AudioRef: "blob://tts/call_w1", Fields: map[string]string{"spoken_text": "Tomorrow will be sunny, ", "unspoken_text": "with a high of nineteen.", "frames_played": "12000", "call_id": "call_w1", "reason": "tts_unavailable"}},
		{Kind: journal.KindToolResult, Fields: map[string]string{"call_id": "call_w1", "outcome": "error", "result_json": `{"error":"tts_unavailable"}`}},
	} {
		j := journal.New(store, journal.FixedClock(at.Add(time.Duration(i)*time.Second)), v)
		if _, err := j.Append(ctx, "c-kitchen-evening", r); err != nil {
			t.Fatalf("append %s: %v", r.Kind, err)
		}
	}
	return store
}

// A reviewer reading the kitchen's evening sees each failure for what it
// was, with what the provider said and which line apologised for it, not a
// blank row and not a barge-in.
//
// verifies SPEC §7
func TestTheConversationPageSaysTheModelAndTheVoiceFailed(t *testing.T) {
	now := func() time.Time { return thursday.Add(20 * time.Hour) }
	h := get(t, newServer(failingKitchen(t), curation.NewMemStore(), fixtureBlobs(t), now), "/conversations/c-kitchen-evening")
	for _, want := range []string{
		"model failed: unavailable",
		"connect: connection refused · apologised in cn_77d0a1b2",
		"voice failed: tts_unavailable",
		"call call_w1 · synthesize",
		"kokoro: 503 Service Unavailable · apologised in cn_5e21c3d4",
		"12000 frames played · cut: tts_unavailable",
	} {
		if !strings.Contains(h, want) {
			t.Errorf("the conversation page does not say %q", want)
		}
	}
}
