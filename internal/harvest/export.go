package harvest

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// row is one JSONL line in the DPO message shape. Chosen is absent until
// curated, so a loader that requires it fails on a raw candidate.
type row struct {
	Prompt   []Message `json:"prompt"`
	Chosen   []Message `json:"chosen,omitempty"`
	Rejected []Message `json:"rejected"`
	Meta     meta      `json:"meta"`
}

// meta is the provenance a row needs to be reviewed, attributed and replayed.
// The heard/unheard split is kept although rejected carries both joined.
type meta struct {
	ID                string   `json:"id"`
	ConversationID    string   `json:"conversation_id"`
	Source            Source   `json:"source"`
	Curated           bool     `json:"curated"`
	Attributed        bool     `json:"attributed"`
	Versions          versions `json:"versions"`
	Seq               Seq      `json:"seq"`
	BargeInPositionMS int      `json:"barge_in_position_ms"`
	RejectedHeard     string   `json:"rejected_heard"`
	RejectedUnheard   string   `json:"rejected_unheard"`
	Heard             string   `json:"heard"`
	HeardSpeaker      string   `json:"heard_speaker"`
	AsSaid            string   `json:"as_said"`
	AsSaidCut         bool     `json:"as_said_cut"`
	Audio             Audio    `json:"audio"`
	Calls             []call   `json:"calls"`

	// Recalled is what the rejected turn was told it remembers, absent when
	// nothing was: a loader must put it in the prompt's context to train
	// the pair faithfully.
	Recalled []journal.Memory `json:"recalled,omitempty"`

	// RecalledConversations are the person's earlier conversations the turn
	// was told of, and HeardAt the time it was told it was (RFC 3339). Both
	// belong in the prompt's context beside Recalled.
	RecalledConversations []journal.Summary `json:"recalled_conversations,omitempty"`
	HeardAt               string            `json:"heard_at,omitempty"`

	// Labels, ChosenVersions and ChosenCalls are an annotation's or a
	// replay's, absent on a barge-in.
	Labels         []string  `json:"labels,omitempty"`
	ChosenVersions *versions `json:"chosen_versions,omitempty"`
	ChosenCalls    []call    `json:"chosen_calls,omitempty"`
}

// versions mirrors journal.Versions field for field, so the struct conversion
// in toRow fails to compile when the journal gains a slot this export lacks.
type versions struct {
	Model      string `json:"model"`
	Prompt     string `json:"prompt"`
	ToolSchema string `json:"tool_schema"`
	STT        string `json:"stt"`
	TTS        string `json:"tts"`
}

type call struct {
	CallID  string `json:"call_id"`
	Tool    string `json:"tool"`
	Args    string `json:"args_json"`
	Outcome string `json:"outcome"`
	Result  string `json:"result_json,omitempty"`
}

// Export writes one JSON object per pair. Only a curated pair carries a chosen
// side; a raw one says meta.curated=false, keeping AsSaid out of a training
// run by accident.
func Export(w io.Writer, pairs []Pair) error {
	enc := json.NewEncoder(w)
	// Transcripts are prose; "&" and "<" in them are not markup.
	enc.SetEscapeHTML(false)
	for _, p := range pairs {
		r, err := toRow(p)
		if err != nil {
			return fmt.Errorf("export %s: %w", p.ID, err)
		}
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("export %s: %w", p.ID, err)
		}
	}
	return nil
}

func toRow(p Pair) (row, error) {
	if p.Curated && p.Chosen == "" {
		return row{}, fmt.Errorf("curated with no chosen side")
	}
	r := row{
		Prompt:   nonNilMessages(p.Prompt),
		Rejected: []Message{{Role: "assistant", Content: p.Rejected + p.RejectedUnheard}},
		Meta: meta{
			ID: p.ID, ConversationID: p.ConversationID, Source: p.Source,
			Curated: p.Curated, Attributed: p.Attributed,
			Versions:          versions(p.Versions),
			Seq:               p.Seq,
			BargeInPositionMS: p.BargeInPositionMS,
			RejectedHeard:     p.Rejected, RejectedUnheard: p.RejectedUnheard,
			Heard: p.Heard, HeardSpeaker: p.HeardSpeaker,
			AsSaid: p.AsSaid, AsSaidCut: p.AsSaidCut,
			Audio:    nonNilAudio(p.Audio),
			Calls:    calls(p.Calls),
			Recalled: p.Recalled,

			RecalledConversations: p.RecalledSummaries,
			HeardAt:               stamp(p.HeardAt),
		},
	}
	if p.Curated {
		r.Chosen = []Message{{Role: "assistant", Content: p.Chosen}}
	}
	if p.ChosenVersions != (journal.Versions{}) {
		v := versions(p.ChosenVersions)
		r.Meta.ChosenVersions = &v
	}
	r.Meta.Labels = p.Labels
	if len(p.ChosenCalls) > 0 {
		r.Meta.ChosenCalls = calls(p.ChosenCalls)
	}
	return r, nil
}

func calls(cs []journal.Call) []call {
	out := make([]call, 0, len(cs))
	for _, c := range cs {
		out = append(out, call{CallID: c.ID, Tool: c.Tool, Args: c.Args, Outcome: c.Outcome, Result: c.Result})
	}
	return out
}

// nonNil* keep an absent list as [] rather than null, so a row's shape does
// not depend on whether a turn happened to have speech.
func nonNilMessages(m []Message) []Message {
	if m == nil {
		return []Message{}
	}
	return m
}

func nonNilAudio(a Audio) Audio {
	if a.Rejected == nil {
		a.Rejected = []string{}
	}
	if a.AsSaid == nil {
		a.AsSaid = []string{}
	}
	return a
}

// stamp is a time as the export writes it, absent when the log had none.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
