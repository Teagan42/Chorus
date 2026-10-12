// Package evalsuite replays a curated set of journalled turns against a
// candidate prompt, model and tool set, and scores what each does against a
// reviewer's written expectations, beside a baseline: the versions the
// journal recorded, asked again, or the take it recorded (SPEC §13,
// ADR-0079).
//
// A suite is YAML, checked in and reviewed like code. Each case names a turn
// as the review UI does, conversation/seq, and says what a good answer does.
// The context a case is asked in is rebuilt from the log as internal/rerun
// reads it, with the prompt and tools its turn ran under resolved by the
// labels it recorded (ADR-0071); a case whose context cannot be resolved is
// skipped, never failed.
package evalsuite

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/registry"
)

// MaxRepetitions bounds how often one case is asked: enough to see a
// sampled model's variance, not enough to spend an evening of GPU on a typo.
const MaxRepetitions = 50

// Suite is one file of cases.
type Suite struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`

	// Repetitions is how often each case is asked of an engine; one when
	// unset. The runner's flag overrides it.
	Repetitions int    `yaml:"repetitions,omitempty"`
	Cases       []Case `yaml:"cases"`
}

// Case is one journalled turn and what a good answer to it does.
type Case struct {
	ID string `yaml:"id"`

	// Ref names the turn as the review UI does: its conversation, a slash,
	// and its utterance's seq.
	Ref string `yaml:"ref"`

	// From is where the case came from: hand, exemplar, annotation,
	// barge-in or replay. Provenance only; it scores nothing.
	From string `yaml:"from,omitempty"`

	// Labels are the reviewer's words for the turn (SPEC §9.2), and Note why
	// the case exists. Neither scores anything.
	Labels []string `yaml:"labels,omitempty"`
	Note   string   `yaml:"note,omitempty"`

	Expect Expect `yaml:"expect"`

	ref    Ref
	checks []Check
}

// Expect is what a good answer to a case does. Each field set is one or
// more checks, each scored pass or fail on every run.
type Expect struct {
	// ToolCalled each need a call to the tool whose arguments match;
	// ToolNotCalled each need none.
	ToolCalled    []ToolMatch `yaml:"tool_called,omitempty"`
	ToolNotCalled []ToolMatch `yaml:"tool_not_called,omitempty"`

	Says *Says `yaml:"says,omitempty"`

	// FirstSpeechMS is the budget from the ask to the first words the
	// engine streams. Unmeasured, as on a recorded take, it is not scored.
	FirstSpeechMS int `yaml:"first_speech_ms,omitempty"`

	// AsksConfirmation true needs a question and no call carrying a
	// confirmation the person never gave; false needs no question.
	AsksConfirmation *bool `yaml:"asks_confirmation,omitempty"`

	// NoInventedEntities needs every Home Assistant entity a call names to
	// be one the log showed the turn, or one KnownEntities lists.
	NoInventedEntities bool     `yaml:"no_invented_entities,omitempty"`
	KnownEntities      []string `yaml:"known_entities,omitempty"`

	// Reference is a reviewer's answer, such as a correction's. Its word
	// overlap is reported; it is a check only with MinReferenceOverlap.
	Reference           string  `yaml:"reference,omitempty"`
	MinReferenceOverlap float64 `yaml:"min_reference_overlap,omitempty"`
}

// ToolMatch is a call to Tool whose arguments hold every value in Args,
// nested objects as subsets, and match every pattern in ArgsLike. Neither
// set matches any call to the tool.
type ToolMatch struct {
	Tool     string            `yaml:"tool"`
	Args     map[string]any    `yaml:"args,omitempty"`
	ArgsLike map[string]string `yaml:"args_like,omitempty"`

	like map[string]*regexp.Regexp
}

// Says constrains what the answer says aloud: each phrase, ignoring case and
// spacing, and a word count.
type Says struct {
	Contains    []string `yaml:"contains,omitempty"`
	NotContains []string `yaml:"not_contains,omitempty"`
	MaxWords    int      `yaml:"max_words,omitempty"`
}

// Ref is a turn: the conversation, and the seq of its utterance.
type Ref struct {
	Conversation string
	Seq          uint64
}

func (r Ref) String() string { return fmt.Sprintf("%s/%d", r.Conversation, r.Seq) }

// Turn is the turn the case names. Set once the suite has parsed.
func (c Case) Turn() Ref { return c.ref }

// Checks is what the case scores on each run, in the order Expect lists
// them. Set once the suite has parsed.
func (c Case) Checks() []Check { return c.checks }

// Load reads and checks the suite at path.
func Load(path string) (Suite, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, fmt.Errorf("read %s: %w", path, err)
	}
	s, err := Parse(b)
	if err != nil {
		return Suite{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// Parse is Load for a file already read. Every problem is named, each with
// its case, rather than the first.
func Parse(b []byte) (Suite, error) {
	var s Suite
	dec := yaml.NewDecoder(bytes.NewReader(b))
	// A misspelt expectation must not pass as no expectation.
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return Suite{}, fmt.Errorf("parse: %w", err)
	}
	var errs []error
	if strings.TrimSpace(s.Name) == "" {
		errs = append(errs, errors.New("no name: a report says which suite it scored"))
	}
	if s.Repetitions < 0 || s.Repetitions > MaxRepetitions {
		errs = append(errs, fmt.Errorf("repetitions %d: want 1 to %d", s.Repetitions, MaxRepetitions))
	}
	if len(s.Cases) == 0 {
		errs = append(errs, errors.New("no cases"))
	}
	seen := map[string]bool{}
	for i := range s.Cases {
		c := &s.Cases[i]
		if seen[c.ID] {
			errs = append(errs, fmt.Errorf("case %q twice: an id names one case in a report", c.ID))
		}
		seen[c.ID] = true
		if err := c.compile(); err != nil {
			errs = append(errs, fmt.Errorf("case %s: %w", c.ID, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return Suite{}, err
	}
	return s, nil
}

var (
	caseID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

	// entityID is a Home Assistant entity: its domain, a dot, its object id.
	entityID = regexp.MustCompile(`^[a-z_]+\.[a-z0-9_]+$`)
)

// provenances is what From may say.
var provenances = []string{"", "hand", "exemplar", "annotation", "barge-in", "replay"}

func (c *Case) compile() error {
	var errs []error
	if !caseID.MatchString(c.ID) {
		errs = append(errs, fmt.Errorf("id %q: want lowercase letters, digits, - and _", c.ID))
	}
	ref, err := ParseRef(c.Ref)
	if err != nil {
		errs = append(errs, err)
	}
	c.ref = ref
	if !slices.Contains(provenances, c.From) {
		errs = append(errs, fmt.Errorf("from %q: want one of %s", c.From, strings.Join(provenances[1:], ", ")))
	}
	for _, l := range c.Labels {
		if !slices.Contains(curation.Labels, curation.Label(l)) {
			errs = append(errs, fmt.Errorf("label %q is not in the annotation vocabulary (SPEC §9.2)", l))
		}
	}
	if err := c.Expect.validate(); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	c.checks = c.Expect.checks()
	if len(c.checks) == 0 {
		return errors.New("expects nothing, so it can neither pass nor fail")
	}
	return nil
}

// ParseRef reads conversation/seq. A conversation id may hold a slash; the
// seq is what follows the last one.
func ParseRef(s string) (Ref, error) {
	i := strings.LastIndex(s, "/")
	if i <= 0 {
		return Ref{}, fmt.Errorf("ref %q: want <conversation>/<seq>, as the review UI names a turn", s)
	}
	seq, err := strconv.ParseUint(s[i+1:], 10, 64)
	if err != nil || seq == 0 {
		return Ref{}, fmt.Errorf("ref %q: want <conversation>/<seq>, the seq a positive number", s)
	}
	return Ref{Conversation: s[:i], Seq: seq}, nil
}

func (e *Expect) validate() error {
	var errs []error
	for i := range e.ToolCalled {
		if err := e.ToolCalled[i].compile(); err != nil {
			errs = append(errs, fmt.Errorf("tool_called: %w", err))
		}
	}
	for i := range e.ToolNotCalled {
		if err := e.ToolNotCalled[i].compile(); err != nil {
			errs = append(errs, fmt.Errorf("tool_not_called: %w", err))
		}
	}
	if s := e.Says; s != nil {
		for _, p := range s.Contains {
			if strings.TrimSpace(p) == "" {
				errs = append(errs, errors.New("says.contains: an empty phrase is in every answer"))
			}
		}
		for _, p := range s.NotContains {
			if strings.TrimSpace(p) == "" {
				errs = append(errs, errors.New("says.not_contains: an empty phrase is in every answer"))
			}
		}
		if s.MaxWords < 0 {
			errs = append(errs, fmt.Errorf("says.max_words %d is negative", s.MaxWords))
		}
	}
	if e.FirstSpeechMS < 0 {
		errs = append(errs, fmt.Errorf("first_speech_ms %d is negative", e.FirstSpeechMS))
	}
	for _, k := range e.KnownEntities {
		if !entityID.MatchString(k) {
			errs = append(errs, fmt.Errorf("known_entities: %q is not a Home Assistant entity id (domain.object_id)", k))
		}
	}
	switch {
	case e.MinReferenceOverlap < 0 || e.MinReferenceOverlap > 1:
		errs = append(errs, fmt.Errorf("min_reference_overlap %v: want 0 to 1", e.MinReferenceOverlap))
	case e.MinReferenceOverlap > 0 && strings.TrimSpace(e.Reference) == "":
		errs = append(errs, errors.New("min_reference_overlap without a reference to overlap"))
	}
	return errors.Join(errs...)
}

func (m *ToolMatch) compile() error {
	switch {
	case m.Tool == "speak":
		return errors.New("speak is speech, not an action (ADR-0003): constrain it with says")
	case strings.HasPrefix(m.Tool, "mcp_"):
	case !declared(m.Tool):
		return fmt.Errorf("%q is not a declared tool; an MCP tool is named mcp_<server>_<tool>", m.Tool)
	}
	m.like = map[string]*regexp.Regexp{}
	var errs []error
	for k, p := range m.ArgsLike {
		re, err := regexp.Compile(p)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s args_like.%s: %w", m.Tool, k, err))
			continue
		}
		m.like[k] = re
	}
	return errors.Join(errs...)
}

func declared(tool string) bool {
	_, ok := registry.Specs[tool]
	return ok
}
