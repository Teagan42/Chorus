package vllm

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
)

// The garage conversation Teagan and Alan had on Thursday evening.
var garageDialogue = []journal.Entry{
	{Kind: journal.EntryHeard, Text: "is the garage door closed", Speaker: "teagan"},
	{Kind: journal.EntrySaid, CallID: "chatcmpl-tool-8b2e6a0d4f9c4715", Text: "Let me check the garage door."},
	{Kind: journal.EntryCall, CallID: "chatcmpl-tool-3f7c1a5e9d2b4860", Tool: "ha_get_state", Args: `{"entity_id":"cover.garage_door"}`},
	{Kind: journal.EntryResult, CallID: "chatcmpl-tool-3f7c1a5e9d2b4860", Tool: "ha_get_state", Outcome: "ok", Result: `{"entity_id":"cover.garage_door","state":"open"}`},
	{Kind: journal.EntrySaid, CallID: "chatcmpl-tool-e41c7a2d905b4f6a", Text: "It's open. Want me to", Cut: true},
	{Kind: journal.EntryHeard, Text: "yes close it", Speaker: "alan"},
}

// vLLM's unstreamed answer, with the reasoning a template left in content.
const garageSummary = `{"id":"chatcmpl-9d4b2f7e1a3c4058","object":"chat.completion","created":1791824400,"model":"Qwen/Qwen3-32B-AWQ","choices":[{"index":0,"message":{"role":"assistant","content":"<think>\n\n</think>\n\nteagan asked whether the garage door was closed; it was open, and the assistant closed it after alan said to.","tool_calls":[]},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":212,"total_tokens":241,"completion_tokens":29}}`

// Summarizing is one unstreamed ask with no tools: the summary prompt, and
// the conversation as it happened with who said each thing, as the Ollama
// engine asks it. Reasoning left in the content is not the summary.
//
// verifies SPEC §5
func TestSummarizeAsksOnceWithTheConversationAndWhoSaidWhat(t *testing.T) {
	f, srv := newFake(t, garageSummary)
	got, err := engineAt(t, srv, Config{}).Summarize(context.Background(), garageDialogue, []string{"teagan", "alan"})
	if err != nil {
		t.Fatalf("summarize: %v", err)
	}
	if want := "teagan asked whether the garage door was closed; it was open, and the assistant closed it after alan said to."; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
	req := decodeSent(t, f.last(t))
	if req.Stream || req.Tools != nil {
		t.Errorf("asked with stream=%v tools=%v, want one unstreamed answer and no tools", req.Stream, req.Tools)
	}
	if req.ChatTemplateKwargs["enable_thinking"] != false {
		t.Errorf("chat_template_kwargs = %v, want thinking off as for a turn", req.ChatTemplateKwargs)
	}
	if len(req.Messages) != 2 || req.Messages[0].Content != chat.SummaryPrompt || req.Messages[1].Content != chat.Transcript(garageDialogue, []string{"teagan", "alan"}) {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if !strings.Contains(req.Messages[1].Content, `the assistant said "It's open. Want me to", and was cut off there`) {
		t.Errorf("transcript:\n%s", req.Messages[1].Content)
	}
}

// A refusal or a failure is an error, not a summary.
//
// verifies SPEC §5
func TestSummarizeRefusesWhatIsNotASummary(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"unavailable": {status: http.StatusServiceUnavailable, body: `{"error":{"message":"The server is loading the model"}}`},
		"error field": {body: `{"error":{"message":"This model's maximum context length is 32768 tokens."}}`},
		"no choices":  {body: `{"choices":[]}`},
		"not json":    {body: `<html>bad gateway</html>`},
	} {
		f, srv := newFake(t, c.body)
		f.status = c.status
		if got, err := engineAt(t, srv, Config{}).Summarize(context.Background(), garageDialogue, []string{"teagan"}); err == nil {
			t.Errorf("%s: summarized %q", name, got)
		}
	}
}
