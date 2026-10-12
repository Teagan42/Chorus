package evalsuite

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
)

// DraftFirstSpeechMS is the budget a draft gives a turn a reviewer called too
// slow: a starting point to tighten, from the measured cascade (SPEC §11).
const DraftFirstSpeechMS = 1500

// Overlap floors a draft sets: a recorded exemplar is the answer nearly word
// for word; a reviewer's correction says what to do in their own words.
const (
	draftExemplarOverlap   = 0.5
	draftCorrectionOverlap = 0.3
)

// Draft is a suite of the turns reviewers have judged, for a person to
// tighten and check in: each exemplar expects the calls it made and its
// words, each fault with a correction and each accepted barge-in pair has
// the reviewer's answer as its reference. One case per turn; an
// annotation's wins over a pair's.
func Draft(ctx context.Context, store journal.Store, decisions curation.Store, name string) (Suite, error) {
	lister, ok := store.(journal.Lister)
	if !ok {
		return Suite{}, fmt.Errorf("draft: %T cannot list conversations", store)
	}
	ids, err := lister.Conversations(ctx)
	if err != nil {
		return Suite{}, err
	}
	slices.Sort(ids)
	s := Suite{Name: name, Description: "Drafted from the review UI's judgements."}
	for _, id := range ids {
		if strings.HasPrefix(id, "device:") || strings.HasPrefix(id, "house:") {
			continue
		}
		cases, err := drafted(ctx, store, decisions, id)
		if err != nil {
			return Suite{}, err
		}
		s.Cases = append(s.Cases, cases...)
	}
	return s, nil
}

func drafted(ctx context.Context, store journal.Store, decisions curation.Store, id string) ([]Case, error) {
	events, err := store.Events(ctx, id)
	if err != nil || len(events) == 0 {
		return nil, err
	}
	turns, err := rerun.Turns(events)
	if err != nil {
		return nil, fmt.Errorf("draft %s: %w", id, err)
	}
	notes, err := decisions.Annotations(ctx, id)
	if err != nil {
		return nil, err
	}
	verdicts, err := decisions.ForConversation(ctx, id)
	if err != nil {
		return nil, err
	}
	res, err := harvest.Scan(ctx, store, id)
	if err != nil {
		return nil, err
	}
	chosen := map[uint64]string{}
	for _, p := range res.Pairs {
		if d, ok := verdicts[p.ID]; ok && d.Status == curation.StatusAccepted && strings.TrimSpace(d.Chosen) != "" {
			chosen[p.Seq.Prompt] = d.Chosen
		}
	}
	var out []Case
	for _, t := range turns {
		ref := Ref{Conversation: id, Seq: t.Seq}
		c := Case{ID: draftID(ref), Ref: ref.String()}
		a, annotated := notes[t.Seq]
		switch {
		case annotated && a.Has(curation.LabelExemplar) && !slices.ContainsFunc(a.Labels, curation.Label.Fault):
			c.From, c.Expect = "exemplar", exemplar(t.Recorded)
		case annotated && a.Faulted():
			c.From, c.Expect = "annotation", corrected(a.ShouldHave)
			if a.Has(curation.LabelTooSlow) {
				c.Expect.FirstSpeechMS = DraftFirstSpeechMS
			}
		case chosen[t.Seq] != "":
			c.From, c.Expect = "barge-in", corrected(chosen[t.Seq])
		default:
			continue
		}
		if annotated {
			for _, l := range a.Labels {
				c.Labels = append(c.Labels, string(l))
			}
			c.Note = strings.TrimSpace(a.ShouldHave)
		}
		c.Note = strings.TrimSpace(strings.Join([]string{fmt.Sprintf("%q", t.Text), c.Note}, " "))
		if len(c.Expect.checks()) > 0 {
			out = append(out, c)
		}
	}
	return out, nil
}

// exemplar expects what a good turn did: each call, with its arguments, and
// its words as a reference.
func exemplar(take rerun.Take) Expect {
	e := Expect{NoInventedEntities: true}
	for _, c := range take.Calls {
		m := ToolMatch{Tool: c.Tool}
		var args map[string]any
		if json.Unmarshal([]byte(c.Args), &args) == nil {
			delete(args, "confirmation")
			m.Args = args
		}
		e.ToolCalled = append(e.ToolCalled, m)
	}
	if said := take.Said(); said != "" {
		e.Reference, e.MinReferenceOverlap = said, draftExemplarOverlap
	}
	return e
}

func corrected(answer string) Expect {
	return Expect{NoInventedEntities: true, Reference: strings.TrimSpace(answer), MinReferenceOverlap: draftCorrectionOverlap}
}

var notIDChar = regexp.MustCompile(`[^a-z0-9_-]+`)

func draftID(r Ref) string {
	return strings.Trim(notIDChar.ReplaceAllString(strings.ToLower(r.Conversation), "-"), "-") + fmt.Sprintf("-%d", r.Seq)
}

// Marshal writes the suite as YAML a person reads and edits.
func (s Suite) Marshal() ([]byte, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
