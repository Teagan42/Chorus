package vllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
)

// maxSummary bounds one unstreamed answer: a couple of sentences, wrapped in
// the response's envelope.
const maxSummary = 1 << 20

// Summarize asks the model, without tools and without streaming, what the
// conversation was about (SPEC §5, ADR-0043). Nobody is waiting on the
// answer, so the context the session gives it is the only bound.
func (e *Engine) Summarize(ctx context.Context, dialogue []journal.Entry, people []string) (string, error) {
	resp, err := e.post(ctx, request{
		Model: e.cfg.Model,
		Messages: []message{
			{Role: "system", Content: chat.SummaryPrompt},
			{Role: "user", Content: chat.Transcript(dialogue, people)},
		},
		Stream:             false,
		MaxTokens:          e.cfg.MaxTokens,
		ChatTemplateKwargs: e.templateKwargs(),
	}, "summary")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var r struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *apiError `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSummary)).Decode(&r); err != nil {
		return "", fmt.Errorf("vllm summary: decode: %w", err)
	}
	if r.Error != nil {
		return "", errors.New("vllm summary: " + r.Error.Message)
	}
	if len(r.Choices) == 0 {
		return "", errors.New("vllm summary: no choices in the answer")
	}
	// Reasoning a model wrote into its content is not the summary.
	return chat.Unthought(r.Choices[0].Message.Content), nil
}
