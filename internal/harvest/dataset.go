package harvest

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/journal"
)

// The two sides of the split a row lands in.
const (
	SplitTrain      = "train"
	SplitValidation = "validation"
)

// SourceExemplar is an SFT row: a turn a reviewer labelled good — exemplar
// (SPEC §9.2). It makes no pair (ADR-0052), so it ships in its own file.
const SourceExemplar Source = "exemplar"

// Options shape an export.
type Options struct {
	// Contexts resolves the versions a row ran under to the system prompt
	// and tool declarations they name. Nil resolves none, so every row
	// says context_missing.
	Contexts contexts.Source

	// Validation is the fraction of conversations held out, from 0 to 1.
	Validation float64
}

// split is the side of the split a conversation lands on: the first eight
// bytes of the SHA-256 of its id, read as a fraction of 2^64, below the
// validation fraction is held out. So a conversation never straddles the
// split, and raising the fraction only moves conversations into validation.
func (o Options) split(conversationID string) string {
	sum := sha256.Sum256([]byte(conversationID))
	if float64(binary.BigEndian.Uint64(sum[:8])) < o.Validation*math.Exp2(64) {
		return SplitValidation
	}
	return SplitTrain
}

// Exemplar is a turn a reviewer marked good — exemplar, and when they did.
type Exemplar struct {
	ConversationID string
	Turn           Turn
	DecidedAt      time.Time
}

// sftRow is one JSONL line in the prompt-completion message shape SFT
// loaders read: what the turn was asked, in context, and what it said and
// called.
type sftRow struct {
	SchemaVersion int             `json:"schema_version"`
	Prompt        []Message       `json:"prompt"`
	Completion    []Message       `json:"completion"`
	Tools         json.RawMessage `json:"tools,omitempty"`
	Meta          sftMeta         `json:"meta"`
}

type sftMeta struct {
	ID             string   `json:"id"`
	ConversationID string   `json:"conversation_id"`
	Source         Source   `json:"source"`
	Attributed     bool     `json:"attributed"`
	Versions       versions `json:"versions"`
	Seq            sftSeq   `json:"seq"`
	Speaker        string   `json:"speaker"`
	Said           string   `json:"said"`
	Unheard        string   `json:"unheard"`
	Audio          sftAudio `json:"audio"`
	Calls          []call   `json:"calls"`

	Recalled              []journal.Memory  `json:"recalled,omitempty"`
	RecalledConversations []journal.Summary `json:"recalled_conversations,omitempty"`
	HeardAt               string            `json:"heard_at,omitempty"`

	provenance
}

type sftSeq struct {
	Prompt uint64 `json:"prompt"`
}

// sftAudio is the exemplar's clips: what was asked and what it said.
type sftAudio struct {
	Ask   string            `json:"ask"`
	Said  []string          `json:"said"`
	Gone  []string          `json:"gone,omitempty"`
	Paths map[string]string `json:"paths,omitempty"`
}

// Dataset is an export's rows, built once and written as JSONL or an
// archive. DPO rows are deduplicated and every row knows its split.
type Dataset struct {
	dpo        []row
	sft        []sftRow
	validation float64
}

// Build turns curated pairs and exemplars into rows. A pair that cannot be
// written fails the build, naming it.
func Build(pairs []Pair, exemplars []Exemplar, opt Options) (Dataset, error) {
	if math.IsNaN(opt.Validation) || opt.Validation < 0 || opt.Validation > 1 {
		return Dataset{}, fmt.Errorf("export: validation fraction %v is not between 0 and 1", opt.Validation)
	}
	d := Dataset{validation: opt.Validation}
	rows := make([]row, 0, len(pairs))
	for _, p := range pairs {
		r, err := toRow(p, opt)
		if err != nil {
			return Dataset{}, fmt.Errorf("export %s: %w", p.ID, err)
		}
		rows = append(rows, r)
	}
	d.dpo = dedupe(rows)
	for _, e := range exemplars {
		r, err := toSFTRow(e, opt)
		if err != nil {
			return Dataset{}, fmt.Errorf("export %s/%d/%s: %w", e.ConversationID, e.Turn.Seq, SourceExemplar, err)
		}
		d.sft = append(d.sft, r)
	}
	return d, nil
}

// Len is how many DPO and SFT rows the dataset holds.
func (d Dataset) Len() (dpo, sft int) { return len(d.dpo), len(d.sft) }

// Head is the dataset's first n rows of each kind.
func (d Dataset) Head(n int) Dataset {
	d.dpo, d.sft = d.dpo[:min(n, len(d.dpo))], d.sft[:min(n, len(d.sft))]
	return d
}

// WriteDPO writes the preference rows in split, or every row for "".
func (d Dataset) WriteDPO(w io.Writer, split string) error {
	return writeJSONL(w, d.dpo, split, func(r row) (string, string) { return r.Meta.ID, r.Meta.Split })
}

// WriteSFT writes the exemplar rows in split, or every row for "".
func (d Dataset) WriteSFT(w io.Writer, split string) error {
	return writeJSONL(w, d.sft, split, func(r sftRow) (string, string) { return r.Meta.ID, r.Meta.Split })
}

func writeJSONL[R any](w io.Writer, rows []R, split string, key func(R) (id, split string)) error {
	enc := json.NewEncoder(w)
	// Transcripts are prose; "&" and "<" in them are not markup.
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		id, s := key(r)
		if split != "" && s != split {
			continue
		}
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("export %s: %w", id, err)
		}
	}
	return nil
}

// sourceRank orders the sources a duplicate is kept from: the barge-in the
// log made first, then the reviewer's note, then their re-run.
var sourceRank = []Source{SourceBargeIn, SourceAnnotation, SourceReplay}

// dedupe keeps one row per preference. Two rows are the same preference
// when they share a conversation, a turn, the context they carry, and the
// same rejected and chosen sides, speech and calls, up to whitespace. The
// kept row is the earliest source in sourceRank, then the first listed; it
// names the rest in meta.duplicates and takes their labels.
func dedupe(rows []row) []row {
	keys := make([]string, len(rows))
	same := map[string][]int{}
	keep := map[string]int{}
	for i, r := range rows {
		k := preference(r)
		keys[i] = k
		same[k] = append(same[k], i)
		j, seen := keep[k]
		if !seen || slices.Index(sourceRank, r.Meta.Source) < slices.Index(sourceRank, rows[j].Meta.Source) {
			keep[k] = i
		}
	}
	out := make([]row, 0, len(keep))
	for i, r := range rows {
		if keep[keys[i]] != i {
			continue
		}
		// The pair's own labels are the caller's slice, not this row's to grow.
		r.Meta.Labels = slices.Clone(r.Meta.Labels)
		for _, j := range same[keys[i]] {
			if j == i {
				continue
			}
			dup := rows[j].Meta
			r.Meta.Duplicates = append(r.Meta.Duplicates, duplicate{ID: dup.ID, Source: dup.Source})
			for _, l := range dup.Labels {
				if !slices.Contains(r.Meta.Labels, l) {
					r.Meta.Labels = append(r.Meta.Labels, l)
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// preference is the key two rows of the same preference share.
func preference(r row) string {
	side := func(ms []Message) string {
		var b strings.Builder
		for _, m := range ms {
			b.WriteString(strings.Join(strings.Fields(m.Content), " "))
			calls, _ := json.Marshal(m.ToolCalls)
			b.Write(calls)
		}
		return b.String()
	}
	return strings.Join([]string{
		r.Meta.ConversationID, fmt.Sprint(r.Meta.Seq.Prompt),
		r.Meta.Context.Prompt, r.Meta.Context.ToolSchema,
		side(r.Rejected), side(r.Chosen),
	}, "\x00")
}

// toSFTRow is an exemplar as SFT trains on it: the turn as generated, heard
// and unheard, with the calls it made beside its speech.
func toSFTRow(e Exemplar, opt Options) (sftRow, error) {
	t := e.Turn
	said := t.Said + t.Unheard
	acts := actions(t.Calls)
	if strings.TrimSpace(said) == "" && len(acts) == 0 {
		return sftRow{}, fmt.Errorf("an exemplar that neither says nor calls anything")
	}
	system, tools, prov := opt.resolve(t.Versions, "", "")
	prov.Verdict, prov.DecidedAt = string(SourceExemplar), stamp(e.DecidedAt)
	prov.Split = opt.split(e.ConversationID)
	audio := sftAudio{Ask: t.AskAudio, Said: t.Audio, Gone: t.Gone}
	if audio.Said == nil {
		audio.Said = []string{}
	}
	return sftRow{
		SchemaVersion: SchemaVersion,
		Prompt:        withSystem(system, t.Prompt),
		Completion:    []Message{{Role: "assistant", Content: said, ToolCalls: toolCalls(acts)}},
		Tools:         tools,
		Meta: sftMeta{
			ID:             fmt.Sprintf("%s/%d/%s", e.ConversationID, t.Seq, SourceExemplar),
			ConversationID: e.ConversationID, Source: SourceExemplar,
			Attributed: t.Attributed, Versions: versions(t.Versions),
			Seq: sftSeq{Prompt: t.Seq}, Speaker: t.Speaker,
			Said: t.Said, Unheard: t.Unheard,
			Audio: audio, Calls: calls(t.Calls),
			Recalled: t.Recalled, RecalledConversations: t.RecalledSummaries,
			HeardAt:    stamp(t.HeardAt),
			provenance: prov,
		},
	}, nil
}
