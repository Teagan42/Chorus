package evalsuite

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"text/tabwriter"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/rerun"
)

// Moved is how far a check's pass rate must fall, or rise, between the
// sides to count as a regression or an improvement: in at least half more
// of its runs. One run against one is a whole run.
const Moved = 0.5

// Gate is when a candidate fails: its score below the baseline's by more
// than Threshold, or more than MaxRegressions checks regressed.
type Gate struct {
	Threshold      float64 `json:"threshold"`
	MaxRegressions int     `json:"max_regressions"`
}

// DefaultGate fails on any regressed check, or the score falling by more
// than 0.05.
var DefaultGate = Gate{Threshold: 0.05, MaxRegressions: 0}

// Report is a suite scored on both sides.
type Report struct {
	Suite     string       `json:"suite"`
	Baseline  SideReport   `json:"baseline"`
	Candidate SideReport   `json:"candidate"`
	Cases     []CaseReport `json:"cases"`

	// Score is each side's mean pass rate over the checks both sides
	// scored, and Compared how many those were.
	Score    Score `json:"score"`
	Compared int   `json:"compared"`

	Regressions  int `json:"regressions"`
	Improvements int `json:"improvements"`
	Skipped      int `json:"skipped"`

	Gate      Gate     `json:"gate"`
	Regressed bool     `json:"regressed"`
	Why       []string `json:"why,omitempty"`
}

// SideReport says what a side ran under.
type SideReport struct {
	Name     string `json:"name"`
	Recorded bool   `json:"recorded,omitempty"`

	// Versions is what its first run ran under, Runs how often each case
	// was asked, and Flaky how many checks passed in some runs only.
	Versions journal.Versions `json:"versions"`
	Runs     int              `json:"runs"`
	Flaky    int              `json:"flaky"`
}

// Score is each side's mean pass rate, and the candidate's change.
type Score struct {
	Baseline  float64 `json:"baseline"`
	Candidate float64 `json:"candidate"`
	Delta     float64 `json:"delta"`
}

// CaseReport is one case on both sides.
type CaseReport struct {
	ID      string `json:"id"`
	Ref     string `json:"ref"`
	From    string `json:"from,omitempty"`
	Asked   string `json:"asked"`
	Skipped string `json:"skipped,omitempty"`

	Checks    []CheckReport `json:"checks,omitempty"`
	Baseline  []RunReport   `json:"baseline,omitempty"`
	Candidate []RunReport   `json:"candidate,omitempty"`
}

// CheckReport is one check's pass rate on each side.
type CheckReport struct {
	Name      string `json:"name"`
	Baseline  Rate   `json:"baseline"`
	Candidate Rate   `json:"candidate"`

	// Change is "regressed" or "improved" when the rate moved by Moved.
	Change string `json:"change,omitempty"`
}

// Rate is how many of a check's runs passed, of those it scored.
type Rate struct {
	Passed int `json:"passed"`
	Scored int `json:"scored"`
}

// Value is the pass rate, false when no run was scored.
func (r Rate) Value() (float64, bool) {
	if r.Scored == 0 {
		return 0, false
	}
	return float64(r.Passed) / float64(r.Scored), true
}

func (r Rate) flaky() bool { return r.Passed > 0 && r.Passed < r.Scored }

func (r Rate) String() string {
	if r.Scored == 0 {
		return "–"
	}
	return fmt.Sprintf("%d/%d", r.Passed, r.Scored)
}

// RunReport is one run, as the JSON report keeps it.
type RunReport struct {
	Seed          int              `json:"seed"`
	Versions      journal.Versions `json:"versions"`
	Said          string           `json:"said"`
	Calls         []rerun.Call     `json:"calls,omitempty"`
	FirstSpeechMS *int64           `json:"first_speech_ms,omitempty"`
	Overlap       *float64         `json:"reference_overlap,omitempty"`
	Cached        bool             `json:"cached,omitempty"`
	Error         string           `json:"error,omitempty"`
	Verdicts      []string         `json:"verdicts"`
}

// Compare scores both sides' runs of every case and applies the gate.
func Compare(s Suite, cases []Resolved, baseline, candidate Side, base, cand map[string][]Run, g Gate) Report {
	rep := Report{
		Suite:     s.Name,
		Baseline:  SideReport{Name: baseline.Name, Recorded: baseline.Recorded},
		Candidate: SideReport{Name: candidate.Name, Recorded: candidate.Recorded},
		Gate:      g,
	}
	var sumBase, sumCand float64
	for _, c := range cases {
		cr := CaseReport{ID: c.Case.ID, Ref: c.Case.Turn().String(), From: c.Case.From, Asked: c.Turn.Text, Skipped: c.Skipped}
		if c.Skipped != "" {
			rep.Skipped++
			rep.Cases = append(rep.Cases, cr)
			continue
		}
		b, k := base[c.Case.ID], cand[c.Case.ID]
		rep.Baseline.saw(b)
		rep.Candidate.saw(k)
		cr.Baseline, cr.Candidate = runReports(c, b), runReports(c, k)
		for _, ch := range c.Case.Checks() {
			chr := CheckReport{Name: ch.Name, Baseline: rate(ch, c.Evidence, b), Candidate: rate(ch, c.Evidence, k)}
			if chr.Baseline.flaky() {
				rep.Baseline.Flaky++
			}
			if chr.Candidate.flaky() {
				rep.Candidate.Flaky++
			}
			bv, bok := chr.Baseline.Value()
			kv, kok := chr.Candidate.Value()
			if bok && kok {
				rep.Compared++
				sumBase += bv
				sumCand += kv
				switch {
				case bv-kv >= Moved:
					chr.Change = "regressed"
					rep.Regressions++
				case kv-bv >= Moved:
					chr.Change = "improved"
					rep.Improvements++
				}
			}
			cr.Checks = append(cr.Checks, chr)
		}
		rep.Cases = append(rep.Cases, cr)
	}
	if rep.Compared > 0 {
		rep.Score.Baseline = sumBase / float64(rep.Compared)
		rep.Score.Candidate = sumCand / float64(rep.Compared)
		rep.Score.Delta = rep.Score.Candidate - rep.Score.Baseline
	}
	if rep.Compared == 0 {
		rep.Why = append(rep.Why, "no check was scored on both sides, so nothing judges the candidate")
	}
	// Rounded, so a mean that is the threshold to the last bit passes.
	if drop := round(rep.Score.Baseline - rep.Score.Candidate); drop > g.Threshold {
		rep.Why = append(rep.Why, fmt.Sprintf("the score fell by %.3f, more than the threshold of %.3f", drop, g.Threshold))
	}
	if rep.Regressions > g.MaxRegressions {
		rep.Why = append(rep.Why, fmt.Sprintf("%s regressed, more than the %d allowed", plural(rep.Regressions, "check"), g.MaxRegressions))
	}
	rep.Regressed = len(rep.Why) > 0
	return rep
}

func round(f float64) float64 { return math.Round(f*1e9) / 1e9 }

func rate(ch Check, ev Evidence, runs []Run) Rate {
	var r Rate
	for _, run := range runs {
		switch ch.Judge(run.Observation, ev) {
		case Pass:
			r.Passed++
			r.Scored++
		case Fail:
			r.Scored++
		}
	}
	return r
}

func runReports(c Resolved, runs []Run) []RunReport {
	out := make([]RunReport, 0, len(runs))
	for _, run := range runs {
		rr := RunReport{
			Seed: run.Seed, Versions: run.Versions, Said: run.Take.Said(), Calls: run.Take.Calls,
			Cached: run.Cached, Error: run.Err,
		}
		if run.Measured {
			ms := run.FirstSpeech.Milliseconds()
			rr.FirstSpeechMS = &ms
		}
		if ref := c.Case.Expect.Reference; ref != "" {
			o := Overlap(ref, rr.Said)
			rr.Overlap = &o
		}
		for _, ch := range c.Case.Checks() {
			rr.Verdicts = append(rr.Verdicts, ch.Judge(run.Observation, c.Evidence).String())
		}
		out = append(out, rr)
	}
	return out
}

// Verdict is the gate's word in one line.
func (r Report) Verdict() string {
	if r.Regressed {
		return "REGRESSED: " + strings.Join(r.Why, "; ")
	}
	return fmt.Sprintf("ok: %d checks compared, %d improved, none regressed beyond the gate", r.Compared, r.Improvements)
}

// WriteJSON writes the report for a machine.
func (r Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// WriteText writes the report as a terminal table: every check, each
// side's pass rate, and what moved.
func (r Report) WriteText(w io.Writer) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "suite %s · %s\nbaseline: %s\ncandidate: %s\n\n", r.Suite, plural(len(r.Cases), "case"), r.Baseline.describe(), r.Candidate.describe())
	fmt.Fprintln(tw, "CASE\tCHECK\tBASELINE\tCANDIDATE\t")
	for _, c := range r.Cases {
		if c.Skipped != "" {
			fmt.Fprintf(tw, "%s\tskipped: %s\t\t\t\n", c.ID, c.Skipped)
			continue
		}
		for i, ch := range c.Checks {
			id := ""
			if i == 0 {
				id = c.ID
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", id, ch.Name, ch.Baseline, ch.Candidate, ch.Change)
		}
	}
	fmt.Fprintf(tw, "\nscore\tbaseline %.3f · candidate %.3f · Δ %+.3f over %s\t\t\t\n", r.Score.Baseline, r.Score.Candidate, r.Score.Delta, plural(r.Compared, "check"))
	if err := tw.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w, r.Verdict())
	return err
}

// WriteMarkdown writes the report for a pull request: the summary, then
// only the checks that moved or fail on the candidate, then the skips.
func (r Report) WriteMarkdown(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "## Eval: %s\n\n", r.Suite)
	fmt.Fprintf(&b, "Baseline %s, candidate %s, over %s.\n\n", r.Baseline.describe(), r.Candidate.describe(), plural(len(r.Cases), "case"))
	fmt.Fprintf(&b, "| | Baseline | Candidate | Δ |\n|---|---|---|---|\n")
	fmt.Fprintf(&b, "| Score over %s | %.3f | %.3f | %+.3f |\n", plural(r.Compared, "check"), r.Score.Baseline, r.Score.Candidate, r.Score.Delta)
	fmt.Fprintf(&b, "| Flaky checks | %d | %d | |\n\n", r.Baseline.Flaky, r.Candidate.Flaky)
	fmt.Fprintf(&b, "**%s**\n\n", r.Verdict())
	var rows []string
	for _, c := range r.Cases {
		for _, ch := range c.Checks {
			kv, ok := ch.Candidate.Value()
			if ch.Change == "" && (!ok || kv == 1) {
				continue
			}
			rows = append(rows, fmt.Sprintf("| `%s` | %s | %s | %s | %s |", c.ID, mdEscape(ch.Name), ch.Baseline, ch.Candidate, ch.Change))
		}
	}
	if len(rows) > 0 {
		b.WriteString("| Case | Check | Baseline | Candidate | |\n|---|---|---|---|---|\n")
		b.WriteString(strings.Join(rows, "\n") + "\n\n")
	}
	for _, c := range r.Cases {
		if c.Skipped != "" {
			fmt.Fprintf(&b, "- `%s` (%s) skipped: %s\n", c.ID, c.Ref, c.Skipped)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func mdEscape(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func (s *SideReport) saw(runs []Run) {
	if s.Runs == 0 && len(runs) > 0 {
		s.Versions = runs[0].Versions
	}
	s.Runs = max(s.Runs, len(runs))
}

func (s SideReport) describe() string {
	if s.Recorded {
		return "the recorded takes"
	}
	v := s.Versions
	return fmt.Sprintf("%s · %s · %s, %s each", v.Model, v.Prompt, v.ToolSchema, plural(s.Runs, "run"))
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
