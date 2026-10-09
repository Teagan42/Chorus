package journal

import (
	"encoding/json"
	"fmt"
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
