package harvest

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
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

	// SpeechOnly marks a pair whose sides carry no tool calls, because
	// nobody said which calls were right (ADR-0054).
	SpeechOnly bool `json:"speech_only,omitempty"`
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
	rejectedCalls, chosenCalls, withCalls := p.TextCalls()
	// Only a re-run's own calls can stand in for speech: an inherited call
	// beside an empty note is no chosen side.
	if p.Curated && p.Chosen == "" && (p.Source != SourceReplay || len(chosenCalls) == 0) {
		return row{}, fmt.Errorf("curated with no chosen side")
	}
	r := row{
		Prompt:   nonNilMessages(p.Prompt),
		Rejected: []Message{{Role: "assistant", Content: p.Rejected + p.RejectedUnheard, ToolCalls: toolCalls(rejectedCalls)}},
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
		r.Chosen = []Message{{Role: "assistant", Content: p.Chosen, ToolCalls: toolCalls(chosenCalls)}}
	}
	r.Meta.SpeechOnly = !withCalls
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

// labelWrongTool is curation.LabelWrongTool: an annotation saying the calls
// were wrong without saying which were right.
const labelWrongTool = "wrong_tool"

// TextCalls is each side's actions as pair text, or withCalls false when
// nobody said which calls were right and the pair trains on speech alone. A
// replay's chosen calls are the re-run's; a correction or a note is about
// what was said, so its chosen side keeps the turn's calls (ADR-0054).
func (p Pair) TextCalls() (rejected, chosen []journal.Call, withCalls bool) {
	rejected = actions(p.Calls)
	switch {
	case p.Source == SourceReplay:
		return rejected, p.ChosenCalls, true
	case slices.Contains(p.Labels, labelWrongTool):
		return nil, nil, false
	default:
		return rejected, rejected, true
	}
}

// actions drops the speak calls, which are the content already (ADR-0003).
func actions(cs []journal.Call) []journal.Call {
	var out []journal.Call
	for _, c := range cs {
		if c.Tool != "speak" {
			out = append(out, c)
		}
	}
	return out
}

func toolCalls(cs []journal.Call) []ToolCall {
	var out []ToolCall
	for _, c := range cs {
		args := json.RawMessage(c.Args)
		if strings.TrimSpace(c.Args) == "" {
			args = json.RawMessage("{}")
		} else if !json.Valid(args) {
			// A model's malformed arguments are kept as it wrote them.
			args, _ = json.Marshal(c.Args)
		}
		out = append(out, ToolCall{Type: "function", Function: Function{Name: c.Tool, Arguments: args}})
	}
	return out
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
