package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/memory"
	"github.com/teaganglenn/chorus/internal/provider/ollama"
	"github.com/teaganglenn/chorus/internal/session"
)

// rememberer is a model that remembers what it is asked to, and otherwise
// answers from what it was told it remembers.
func rememberer(fact string) *scriptEngine {
	done := session.TurnEnd{FinishReason: "stop", Completion: "{}"}
	return &scriptEngine{decide: func(in session.Input) []session.Action {
		last := in.Dialogue[len(in.Dialogue)-1]
		switch {
		case last.Kind == journal.EntryResult:
			return []session.Action{session.SpeechDelta{CallID: "call_s1", Text: "Got it.", Last: true}, done}
		case last.Text == "remember that i take oat milk in my coffee":
			return []session.Action{session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"` + fact + `"}`}, done}
		}
		return []session.Action{done}
	}}
}

// Alan tells the kitchen about the oat milk. The daemon restarts overnight,
// and the next morning's question is asked with the oat milk remembered.
//
// verifies SPEC §5
func TestTheKitchenRemembersAlanAfterARestart(t *testing.T) {
	const fact = "Takes oat milk in coffee."
	memories := memory.NewMemStore()

	yesterday := newRig(t, inventory(), func(d *deps) {
		d.engine = rememberer(fact)
		d.Memories = memories
	})
	dev := yesterday.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	yesterday.utter(t, dev, yesterday.line("remember that i take oat milk in my coffee", alan))
	if r := yesterday.store.awaitKind(t, journal.KindToolResult, 1); r.Fields["outcome"] != "ok" {
		t.Fatalf("remember = %v, want ok", r.Fields)
	}
	yesterday.cancel()
	if err := yesterday.exit(t); err != nil {
		t.Fatalf("yesterday's daemon: %v", err)
	}

	eng := rememberer(fact)
	today := newRig(t, inventory(), func(d *deps) {
		d.engine = eng
		d.Memories = memories
	})
	dev = today.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	today.utter(t, dev, today.line("how do i take my coffee", alan))
	recalled := today.store.awaitKind(t, journal.KindMemoryRecalled, 1)
	await(t, "this morning's ask", func() bool { return len(eng.heard()) > 0 })

	got := eng.heard()[0].Memories
	if len(got) != 1 || got[0].Person != "alan" || got[0].Fact != fact {
		t.Fatalf("this morning's ask was told %+v, want Alan's oat milk", got)
	}
	if recalled.Fields["person"] != "alan" || recalled.Fields["memories_json"] != journal.EncodeMemories(got) {
		t.Errorf("memory_recalled = %v, not what the model was told", recalled.Fields)
	}
}

// On a fresh install nobody is enrolled, so every voice is a guest, the
// television's included. It says "remember that", and the call is refused
// with nothing kept and nothing recalled.
//
// verifies SPEC §5
func TestAGuestCannotTeachTheKitchenAnything(t *testing.T) {
	memories := memory.NewMemStore()
	r := newRig(t, inventory(), func(d *deps) {
		d.speakers, d.household = nil, nil
		d.engine = rememberer("Takes oat milk in coffee.")
		d.Memories = memories
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("remember that i take oat milk in my coffee", stranger))

	res := r.store.awaitKind(t, journal.KindToolResult, 1)
	if want := map[string]string{"call_id": "call_r1", "outcome": "error", "result_json": `{"error":"unidentified_speaker"}`}; !reflect.DeepEqual(res.Fields, want) {
		t.Errorf("result = %v, want %v", res.Fields, want)
	}
	for _, person := range []string{"", "alan"} {
		if got, _ := memories.Recall(context.Background(), person, memory.RecallLimit); len(got) != 0 {
			t.Errorf("the television left %+v", got)
		}
	}
	if n := len(r.store.ofKind(journal.KindMemoryRecalled)); n != 0 {
		t.Errorf("a guest's turn recorded %d recalls", n)
	}
}

// summarizer is the model writing what a conversation was about.
type summarizer struct {
	mu     sync.Mutex
	people [][]string
}

func (z *summarizer) Summarize(_ context.Context, dialogue []journal.Entry, people []string) (string, error) {
	z.mu.Lock()
	defer z.mu.Unlock()
	z.people = append(z.people, people)
	return "alan asked whether the garage door was closed; the assistant said it was.", nil
}

// Alan asks the garage about the door on Monday at noon, and the daemon
// shuts down with the conversation still open. The summary is written and
// kept before the daemon exits. Tuesday, asked what he asked yesterday, the
// model is told the time and Monday's conversation.
//
// verifies SPEC §5
func TestAlanIsToldWhatHeAskedYesterday(t *testing.T) {
	memories := memory.NewMemStore()
	sum := &summarizer{}
	monday := newRig(t, inventory(), func(d *deps) {
		d.Memories, d.summarizer = memories, sum
	})
	dev := monday.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	monday.utter(t, dev, monday.line("is the garage door closed", alan))
	monday.store.awaitKind(t, journal.KindModelCompleted, 1)
	monday.cancel()
	if err := monday.exit(t); err != nil {
		t.Fatalf("monday's daemon: %v", err)
	}
	summarized := monday.store.ofKind(journal.KindConversationSummarized)
	if len(summarized) != 1 || summarized[0].Fields["people_json"] != `["alan"]` {
		t.Fatalf("summarized %+v, want once, for Alan", summarized)
	}

	tuesday := epoch.Add(24 * time.Hour)
	eng := &scriptEngine{acts: []session.Action{session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	today := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Memories, d.summarizer = eng, memories, sum
		d.Clock = journal.FixedClock(tuesday)
	})
	dev = today.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	today.utter(t, dev, today.line("what did i ask you yesterday", alan))
	await(t, "tuesday's ask", func() bool { return len(eng.heard()) > 0 })

	in := eng.heard()[0]
	want := []journal.Summary{{
		ConversationID: summarized[0].ConversationID, At: epoch,
		Text: "alan asked whether the garage door was closed; the assistant said it was.",
	}}
	if !in.Now.Equal(tuesday) || len(in.Summaries) != 1 || !in.Summaries[0].At.Equal(epoch) ||
		in.Summaries[0].Text != want[0].Text || in.Summaries[0].ConversationID != want[0].ConversationID {
		t.Errorf("tuesday's ask was told %v and %+v, want %v and %+v", in.Now, in.Summaries, tuesday, want)
	}
}

// Without anywhere to keep memories, no conversation is summarized.
//
// verifies SPEC §5
func TestNoMemoryMeansNoSummaries(t *testing.T) {
	sum := &summarizer{}
	r := newRig(t, inventory(), func(d *deps) { d.summarizer = sum })
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("is the garage door closed", alan))
	r.store.awaitKind(t, journal.KindModelCompleted, 1)
	r.cancel()
	if err := r.exit(t); err != nil {
		t.Fatalf("daemon: %v", err)
	}
	if n := len(r.store.ofKind(journal.KindConversationSummarized)); n != 0 || len(sum.people) != 0 {
		t.Errorf("summarized %d times with no memory", n)
	}
}

// garageWords is an embedding model that knows one topic: how much a text
// is about getting into the garage.
type garageWords struct{}

func (garageWords) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		t = strings.ToLower(t)
		out[i] = []float32{float32(strings.Count(t, "garage") + strings.Count(t, "code")), 0.05}
	}
	return out, nil
}

func (garageWords) EmbedModel() string { return "nomic-embed-text" }

// Alan has asked the house to remember two dozen things since he told it
// the garage code in August. Asked for the code, the daemon recalls it by
// relevance, records which model chose, and the model is told it.
//
// verifies SPEC §5
func TestAlanIsToldTheGarageCodeOutOfTwoDozenMemories(t *testing.T) {
	memories := memory.NewMemStore()
	august := epoch.Add(-60 * 24 * time.Hour)
	code := memory.Memory{ID: "m_9a7e4512", Person: "alan", Fact: "The garage door code is 4512.", At: august}
	if err := memories.Remember(context.Background(), code); err != nil {
		t.Fatalf("remember: %v", err)
	}
	for i := range 24 {
		m := memory.Memory{
			ID: fmt.Sprintf("m_alan%04d", i), Person: "alan", At: august.Add(time.Duration(i+1) * 24 * time.Hour),
			Fact: fmt.Sprintf("Watered the porch plants on day %d of the month.", i+1),
		}
		if err := memories.Remember(context.Background(), m); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}
	eng := &scriptEngine{acts: []session.Action{session.TurnEnd{FinishReason: "stop", Completion: "{}"}}}
	r := newRig(t, inventory(), func(d *deps) {
		d.engine, d.Memories, d.embedder = eng, memories, garageWords{}
	})
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("what's the code for the garage", alan))
	recalled := r.store.awaitKind(t, journal.KindMemoryRecalled, 1)
	await(t, "the ask", func() bool { return len(eng.heard()) > 0 })

	told := eng.heard()[0].Memories
	if len(told) != memory.RecallLimit || !slices.Contains(told, code.Recalled()) {
		t.Errorf("the model was told %d memories without the code: %+v", len(told), told)
	}
	if recalled.Fields["ranked_by"] != "nomic-embed-text" || recalled.Fields["memories_json"] != journal.EncodeMemories(told) {
		t.Errorf("memory_recalled = %v, want what the model was told, ranked by the embedding model", recalled.Fields)
	}
}

// recordedOllama answers every /api/chat with one stream the household's
// Ollama sent, as the real engine reads it.
type recordedOllama struct{ stream []byte }

func (o recordedOllama) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK, Status: "200 OK", Request: r, Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(o.stream)),
	}, nil
}

// Alan asks the kitchen for the garage code he asked the house to remember.
// The real engine runs over the stream qwen3:14b sent when it answered from
// memory without calling speak: "speak", then the code, as content. Alan
// hears the code, and the log says it was said.
//
// verifies SPEC §4.1, §5
func TestAlanHearsTheGarageCodeTheModelWroteAsContent(t *testing.T) {
	alanHearsFromContent(t, "answered_in_content.ndjson",
		memory.Memory{ID: "m_9a7e4512", Person: "alan", Fact: "The garage door code is 4512.", At: epoch.Add(-60 * 24 * time.Hour)},
		"what's the code for the garage", "The garage door code is 4512.")
}

// Asked how he takes his coffee, qwen3:14b wrote its reply as one line of
// content, the words quoted after the tool's name. Alan hears the question
// it asked back, without "speak" or the quotes.
//
// verifies SPEC §4.1, §5
func TestAlanHearsTheQuestionTheModelWroteAfterTheToolsName(t *testing.T) {
	alanHearsFromContent(t, "question_in_content.ndjson",
		memory.Memory{ID: "m_c0ffee01", Person: "alan", Fact: "Alan takes his coffee with oat milk.", At: epoch.Add(-14 * 24 * time.Hour)},
		"how do I take my coffee",
		"Would you like instructions on how to brew your coffee, or are you looking for something else?")
}

// alanHearsFromContent runs the real engine over a stream the household's
// Ollama sent, with what Alan asked the house to remember, and checks that
// what he hears in the kitchen, and what the log says was said, is want.
func alanHearsFromContent(t *testing.T, fixture string, remembered memory.Memory, words, want string) {
	t.Helper()
	stream, err := os.ReadFile(filepath.Join("..", "..", "internal", "provider", "ollama", "testdata", fixture))
	if err != nil {
		t.Fatalf("read stream: %v", err)
	}
	eng, err := ollama.New(ollama.Config{
		BaseURL: "http://ollama.invalid:11434", Model: "qwen3:14b",
		HTTP: &http.Client{Transport: recordedOllama{stream: stream}},
	})
	if err != nil {
		t.Fatalf("engine: %v", err)
	}
	memories := memory.NewMemStore()
	if err := memories.Remember(context.Background(), remembered); err != nil {
		t.Fatalf("remember: %v", err)
	}
	// An answer longer than the first two slices is paced onto the link by
	// the clock, which the rig's timers never advance; the wall's do.
	r := newRig(t, inventory(), func(d *deps) { d.engine, d.Memories, d.Timers = eng, memories, wallTimers{} })
	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line(words, alan))

	dev.AwaitTTS(t, 2*len(want))
	dev.PlayAll(t)
	if spoken := r.store.awaitKind(t, journal.KindSpeechSpoken, 1); spoken.Fields["text"] != want {
		t.Errorf("speech_spoken = %v, want %q and not the tool's name", spoken.Fields, want)
	}
}
