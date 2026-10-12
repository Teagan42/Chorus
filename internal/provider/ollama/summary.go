package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/session"
)

// SummaryPrompt is what the model is told when a conversation ends and it
// writes what the conversation was about, for the people in it to be told
// in their later ones (SPEC §5, ADR-0043).
const SummaryPrompt = chat.SummaryPrompt

// The engine also writes what each conversation was about when it ends.
var _ session.Summarizer = (*Engine)(nil)

// Summarize asks the model, without tools and without streaming, what the
// conversation was about. Nobody is waiting on the answer, so the context
// the session gives it is the only bound.
func (e *Engine) Summarize(ctx context.Context, dialogue []journal.Entry, people []string) (string, error) {
	body, err := json.Marshal(request{
		Model: e.cfg.Model,
		Messages: []message{
			{Role: "system", Content: SummaryPrompt},
			{Role: "user", Content: chat.Transcript(dialogue, people)},
		},
		Stream:    false,
		Think:     e.cfg.Think,
		KeepAlive: e.cfg.KeepAlive,
	})
	if err != nil {
		return "", fmt.Errorf("ollama: encode summary request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("ollama: build summary request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama summary: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, errBody))
		return "", fmt.Errorf("ollama summary: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	// Unstreamed, the answer is one chunk with done set.
	var c chunk
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxLine)).Decode(&c); err != nil {
		return "", fmt.Errorf("ollama summary: decode: %w", err)
	}
	if c.Error != "" {
		return "", errors.New("ollama summary: " + c.Error)
	}
	return chat.Unthought(c.Message.Content), nil
}

// because is why an announcement was said, in the summary's words.
func because(a *journal.Announcement) string { return chat.Because(a) }
