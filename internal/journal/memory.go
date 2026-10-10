package journal

import (
	"encoding/json"
	"fmt"
	"time"
)

// Memory is one thing the model is told it remembers (SPEC §5). The memory
// store owns it; the log keeps what a turn was given, so a replay asks the
// model with the same memories whatever the store says now.
type Memory struct {
	ID     string `json:"id"`
	Person string `json:"person"`
	Fact   string `json:"fact"`

	// Shareable marks a memory the rest of the household may be told.
	Shareable bool `json:"shareable,omitempty"`
}

// EncodeMemories is what memory_recalled records. An empty list encodes as
// [], which says nothing is remembered rather than nothing was recalled.
func EncodeMemories(ms []Memory) string {
	if ms == nil {
		ms = []Memory{}
	}
	b, err := json.Marshal(ms)
	if err != nil {
		// Four strings and a bool cannot fail to encode.
		panic(fmt.Sprintf("encode memories: %v", err))
	}
	return string(b)
}

// Summary is what one of a person's earlier conversations was about, as the
// model wrote it when that conversation ended (SPEC §5).
type Summary struct {
	ConversationID string    `json:"conversation_id"`
	At             time.Time `json:"at"`
	Text           string    `json:"text"`
}

// EncodeSummaries is what memory_recalled records beside the memories.
func EncodeSummaries(ss []Summary) string {
	if ss == nil {
		ss = []Summary{}
	}
	b, err := json.Marshal(ss)
	if err != nil {
		// Two strings and a time cannot fail to encode.
		panic(fmt.Sprintf("encode summaries: %v", err))
	}
	return string(b)
}

// decodeSummaries reads summaries_json, which logs from before summaries
// leave empty.
func decodeSummaries(s string) ([]Summary, error) {
	if s == "" {
		return nil, nil
	}
	var ss []Summary
	if err := json.Unmarshal([]byte(s), &ss); err != nil {
		return nil, fmt.Errorf("summaries_json: %w", err)
	}
	if len(ss) == 0 {
		return nil, nil
	}
	return ss, nil
}

func decodeMemories(s string) ([]Memory, error) {
	var ms []Memory
	if err := json.Unmarshal([]byte(s), &ms); err != nil {
		return nil, fmt.Errorf("memories_json: %w", err)
	}
	if len(ms) == 0 {
		return nil, nil
	}
	return ms, nil
}
