package curation

import (
	"fmt"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Call is one tool call a re-run made, as Replay compared it.
type Call struct {
	Tool string `json:"tool"`
	Args string `json:"args"`
}

// Promotion is a re-run's take a reviewer judged better than what the turn
// recorded: the chosen side of a replay pair (SPEC §9.2). The journal never
// held it, so it is kept whole, with the prompt that produced it.
type Promotion struct {
	ConversationID string
	Seq            uint64

	Speech string
	Calls  []Call

	// Versions is what the re-run ran under, and SystemPrompt the edited
	// prompt itself, so a trainer can say where the chosen side came from.
	Versions     journal.Versions
	SystemPrompt string

	PromotedAt time.Time
}

func (p Promotion) validate() error {
	if p.ConversationID == "" {
		return fmt.Errorf("curation: promotion of turn %d without a conversation", p.Seq)
	}
	if p.Seq == 0 {
		return fmt.Errorf("curation: promotion in %s without a turn", p.ConversationID)
	}
	if strings.TrimSpace(p.Speech) == "" {
		return fmt.Errorf("curation: promotion %s/%d says nothing", p.ConversationID, p.Seq)
	}
	return nil
}
