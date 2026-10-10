package harvest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Negative is one stage-two wake rejection as the wake-word corpus takes it:
// a hard negative, auto-labelled from the log (SPEC §9.3). Like a pair, it
// is derived on read and never stored.
type Negative struct {
	// ID is the device log and the rejection's seq, as a pair's id is.
	ID             string
	ConversationID string
	Seq            uint64
	Satellite      string
	At             time.Time

	// Reason is the gate that rejected it: low_confidence, no_speech or
	// unknown_speaker.
	Reason string

	// Audio is the processed stream; SecondAudio the XMOS's lighter output
	// of the same span, empty from a one-channel device (ADR-0050).
	Audio, SecondAudio string

	// Confirmed is a reviewer having listened and agreed it is no wake.
	Confirmed bool
}

// Held reports a rejection that passed the wake model and the speech check
// and failed only on the voice: likely the wake word in a guest's mouth,
// which as a negative would teach the model to miss it (ADR-0058).
func (n Negative) Held() bool { return n.Reason == "unknown_speaker" }

// Negatives returns every wake rejection in a log, in log order. Rejections
// are written to a satellite's own log, device:<satellite>.
func Negatives(ctx context.Context, store journal.Store, conversationID string) ([]Negative, error) {
	events, err := store.Events(ctx, conversationID)
	if err != nil {
		return nil, fmt.Errorf("negatives %s: %w", conversationID, err)
	}
	var out []Negative
	for _, e := range events {
		if e.Kind != journal.KindWakeRejected {
			continue
		}
		out = append(out, Negative{
			ID:             fmt.Sprintf("%s/%d", conversationID, e.Seq),
			ConversationID: conversationID,
			Seq:            e.Seq,
			Satellite:      strings.TrimPrefix(conversationID, "device:"),
			At:             e.At.UTC(),
			Reason:         e.Fields["reason"],
			Audio:          e.AudioRef,
			SecondAudio:    e.Fields["second_audio_ref"],
		})
	}
	return out, nil
}

// negativeRow is one line of the wake corpus. Label 0 is a negative, as
// wake-word trainers count; audio is by blob ref, as a pair's is.
type negativeRow struct {
	ID             string `json:"id"`
	Label          int    `json:"label"`
	Source         string `json:"source"`
	Reason         string `json:"reason"`
	Satellite      string `json:"satellite"`
	At             string `json:"at"`
	Audio          string `json:"audio"`
	SecondAudio    string `json:"second_audio"`
	Confirmed      bool   `json:"confirmed"`
	ConversationID string `json:"conversation_id"`
	Seq            uint64 `json:"seq"`
}

// ExportNegatives writes one JSON object per rejection.
func ExportNegatives(w io.Writer, ns []Negative) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	for _, n := range ns {
		r := negativeRow{
			ID: n.ID, Source: "stage2_reject", Reason: n.Reason, Satellite: n.Satellite, At: stamp(n.At),
			Audio: n.Audio, SecondAudio: n.SecondAudio, Confirmed: n.Confirmed,
			ConversationID: n.ConversationID, Seq: n.Seq,
		}
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("export %s: %w", n.ID, err)
		}
	}
	return nil
}
