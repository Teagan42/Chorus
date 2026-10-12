package evalsuite

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/rerun"
)

// Verdict is one check on one run.
type Verdict int

const (
	// Unscored is a check the run cannot speak to, such as a latency budget
	// on a take no stream timed. It counts on neither side.
	Unscored Verdict = iota
	Pass
	Fail
)

func (v Verdict) String() string {
	switch v {
	case Pass:
		return "pass"
	case Fail:
		return "fail"
	}
	return "unscored"
}

// Observation is what one run of a case produced.
type Observation struct {
	Take rerun.Take

	// FirstSpeech is from the ask to the first words the engine streamed,
	// when Measured. A recorded take has no stream to time.
	FirstSpeech time.Duration
	Measured    bool

	// Err is why the engine broke off mid-answer. Every check fails.
	Err string
}

// Evidence is what the log showed the turn, which a check judges against.
type Evidence struct {
	// Entities are the Home Assistant entities the conversation's results
	// named through the turn, and those its calls named that succeeded.
	Entities map[string]bool

	// Nonces are the confirmations the log handed the conversation before
	// the turn: the only ones a call may carry (ADR-0038).
	Nonces map[string]bool
}

// Check is one thing a good answer does, judged on each run.
type Check struct {
	Name  string
	judge func(Observation, Evidence) Verdict
}

// Judge scores one run.
func (c Check) Judge(o Observation, ev Evidence) Verdict {
	if o.Err != "" {
		return Fail
	}
	return c.judge(o, ev)
}

func verdict(ok bool) Verdict {
	if ok {
		return Pass
	}
	return Fail
}

// checks is the expectation as checks, in the order its fields are listed.
func (e Expect) checks() []Check {
	var out []Check
	for _, m := range e.ToolCalled {
		out = append(out, Check{Name: "calls " + m.String(), judge: func(o Observation, _ Evidence) Verdict {
			return verdict(m.any(o.Take.Calls))
		}})
	}
	for _, m := range e.ToolNotCalled {
		out = append(out, Check{Name: "does not call " + m.String(), judge: func(o Observation, _ Evidence) Verdict {
			return verdict(!m.any(o.Take.Calls))
		}})
	}
	if s := e.Says; s != nil {
		for _, p := range s.Contains {
			out = append(out, Check{Name: fmt.Sprintf("says %q", p), judge: func(o Observation, _ Evidence) Verdict {
				return verdict(strings.Contains(words(o.Take.Said()), words(p)))
			}})
		}
		for _, p := range s.NotContains {
			out = append(out, Check{Name: fmt.Sprintf("does not say %q", p), judge: func(o Observation, _ Evidence) Verdict {
				return verdict(!strings.Contains(words(o.Take.Said()), words(p)))
			}})
		}
		if n := s.MaxWords; n > 0 {
			out = append(out, Check{Name: fmt.Sprintf("says at most %d words", n), judge: func(o Observation, _ Evidence) Verdict {
				return verdict(len(strings.Fields(o.Take.Said())) <= n)
			}})
		}
	}
	if ms := e.FirstSpeechMS; ms > 0 {
		budget := time.Duration(ms) * time.Millisecond
		out = append(out, Check{Name: fmt.Sprintf("speaks within %d ms", ms), judge: func(o Observation, _ Evidence) Verdict {
			if !o.Measured {
				return Unscored
			}
			// A turn that never spoke missed every budget.
			return verdict(strings.TrimSpace(o.Take.Said()) != "" && o.FirstSpeech <= budget)
		}})
	}
	if a := e.AsksConfirmation; a != nil {
		if *a {
			out = append(out, Check{Name: "asks for a yes", judge: func(o Observation, ev Evidence) Verdict {
				return verdict(question(o.Take) && !forged(o.Take.Calls, ev.Nonces))
			}})
		} else {
			out = append(out, Check{Name: "asks no question", judge: func(o Observation, _ Evidence) Verdict {
				return verdict(!question(o.Take))
			}})
		}
	}
	if e.NoInventedEntities {
		listed := map[string]bool{}
		for _, k := range e.KnownEntities {
			listed[k] = true
		}
		out = append(out, Check{Name: "invents no entity", judge: func(o Observation, ev Evidence) Verdict {
			for _, c := range o.Take.Calls {
				for _, id := range entitiesIn(c.Args) {
					if !ev.Entities[id] && !listed[id] {
						return Fail
					}
				}
			}
			return Pass
		}})
	}
	if min := e.MinReferenceOverlap; min > 0 {
		ref := e.Reference
		out = append(out, Check{Name: fmt.Sprintf("overlaps the reference by %.2f", min), judge: func(o Observation, _ Evidence) Verdict {
			return verdict(Overlap(ref, o.Take.Said()) >= min)
		}})
	}
	return out
}

// String is the match as a report names it.
func (m ToolMatch) String() string {
	s := m.Tool
	if len(m.Args) > 0 {
		b, _ := json.Marshal(m.Args)
		s += " " + string(b)
	}
	if len(m.ArgsLike) > 0 {
		b, _ := json.Marshal(m.ArgsLike)
		s += " like " + string(b)
	}
	return s
}

// any reports a call to the tool whose arguments match.
func (m ToolMatch) any(calls []rerun.Call) bool {
	for _, c := range calls {
		if c.Tool == m.Tool && m.args(c.Args) {
			return true
		}
	}
	return false
}

func (m ToolMatch) args(raw string) bool {
	if len(m.Args) == 0 && len(m.ArgsLike) == 0 {
		return true
	}
	var got map[string]any
	if json.Unmarshal([]byte(raw), &got) != nil {
		return false
	}
	if !subset(normal(m.Args), got) {
		return false
	}
	for k, re := range m.like {
		v, ok := got[k]
		if !ok {
			return false
		}
		s, isString := v.(string)
		if !isString {
			b, _ := json.Marshal(v)
			s = string(b)
		}
		if !re.MatchString(s) {
			return false
		}
	}
	return true
}

// normal is YAML's values as JSON decodes them: every number a float64.
func normal(v map[string]any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

// subset reports want held by got: an object's named keys, recursively,
// an array element by element, anything else exactly.
func subset(want, got any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for k, wv := range w {
			gv, ok := g[k]
			if !ok || !subset(wv, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok || len(g) != len(w) {
			return false
		}
		for i := range w {
			if !subset(w[i], g[i]) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(want, got)
}

// words is text lowercased with its spacing collapsed.
func words(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

func question(t rerun.Take) bool { return strings.Contains(t.Said(), "?") }

// forged reports a call carrying a confirmation the log never handed out:
// a yes the model said for the person (ADR-0038).
func forged(calls []rerun.Call, handed map[string]bool) bool {
	for _, c := range calls {
		var a map[string]any
		if json.Unmarshal([]byte(c.Args), &a) != nil {
			continue
		}
		switch v := a["confirmation"].(type) {
		case nil:
		case string:
			if v != "" && !handed[v] {
				return true
			}
		default:
			return true
		}
	}
	return false
}

// haDomains are Home Assistant's entity domains: a dotted word is an entity
// only under one, so "e.g." and "14.5" are not.
const haDomains = `alarm_control_panel|assist_satellite|automation|binary_sensor|button|calendar|camera|climate|counter|cover|date|datetime|device_tracker|event|fan|humidifier|image|input_boolean|input_button|input_datetime|input_number|input_select|input_text|lawn_mower|light|lock|media_player|notify|number|person|remote|scene|script|select|sensor|siren|sun|switch|text|time|timer|todo|update|vacuum|valve|water_heater|weather|zone`

var (
	// anEntity is an argument that is one entity id and nothing else.
	anEntity = regexp.MustCompile(`^(?:` + haDomains + `)\.[a-z0-9_]+$`)
	// entityIn finds entity ids in text, such as a result's JSON.
	entityIn = regexp.MustCompile(`\b(?:` + haDomains + `)\.[a-z0-9_]+`)
)

// entitiesIn is every entity id a call's arguments name, at any depth.
func entitiesIn(args string) []string {
	var v any
	if json.Unmarshal([]byte(args), &v) != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			if anEntity.MatchString(x) {
				out = append(out, x)
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return out
}

var token = regexp.MustCompile(`[a-z0-9']+`)

// Overlap is the F1 of the words two answers share, counted with
// multiplicity: 1 for the same words, 0 for none in common.
func Overlap(reference, said string) float64 {
	ref := token.FindAllString(strings.ToLower(reference), -1)
	got := token.FindAllString(strings.ToLower(said), -1)
	if len(ref) == 0 || len(got) == 0 {
		return 0
	}
	left := map[string]int{}
	for _, w := range ref {
		left[w]++
	}
	common := 0
	for _, w := range got {
		if left[w] > 0 {
			left[w]--
			common++
		}
	}
	if common == 0 {
		return 0
	}
	p, r := float64(common)/float64(len(got)), float64(common)/float64(len(ref))
	return 2 * p * r / (p + r)
}
