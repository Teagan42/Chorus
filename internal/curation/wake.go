package curation

import (
	"fmt"
	"time"
)

// WakeStatus is a reviewer's word on a rejected wake. Unreviewed is the
// absence of a verdict, as with a pair.
type WakeStatus string

const (
	// WakeConfirmed is a reviewer having listened and agreed it is no wake.
	WakeConfirmed WakeStatus = "confirmed"
	// WakeDiscarded keeps the rejection out of the wake-word corpus.
	WakeDiscarded WakeStatus = "discarded"
)

// WakeVerdict is a reviewer's word on one stage-two rejection, the hard
// negative SPEC §9.3 auto-labels. A rejection is its device log and seq.
// The last write wins; an empty status returns it to unreviewed.
type WakeVerdict struct {
	ConversationID string
	Seq            uint64
	Status         WakeStatus
	JudgedAt       time.Time
}

func (v WakeVerdict) validate() error {
	if v.ConversationID == "" {
		return fmt.Errorf("curation: wake verdict on #%d without a log", v.Seq)
	}
	if v.Seq == 0 {
		return fmt.Errorf("curation: wake verdict in %s without an event", v.ConversationID)
	}
	switch v.Status {
	case "", WakeConfirmed, WakeDiscarded:
		return nil
	}
	return fmt.Errorf("curation: wake verdict %s/%d with status %q", v.ConversationID, v.Seq, v.Status)
}
