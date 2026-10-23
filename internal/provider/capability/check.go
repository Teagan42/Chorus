// Package capability is what each model provider declares it can do, and
// the check that refuses a pipeline whose providers cannot do what the
// architecture needs (SPEC §10, ADR-0013, ADR-0102).
//
// The declarations and needs are generated from schema/provider.cue. A
// provider returns its own from Capabilities, so a provider that is wired
// is a provider that is declared.
package capability

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Declarer is a provider that says what it can do.
type Declarer interface {
	Capabilities() Declaration
}

// Of is the declaration the schema holds for a provider. It panics on one it
// does not, which only a provider package naming itself wrongly can reach,
// and its own tests do.
func Of(role Role, name string) Declaration {
	d, ok := Declared[role][name]
	if !ok {
		panic(fmt.Sprintf("capability: no %s provider %q in schema/provider.cue", role, name))
	}
	return d
}

// Pipeline is the providers one process runs, a role each, and what it is
// configured to ask of them. A role it has no provider for, such as an
// optional sidecar left unset, has no needs.
type Pipeline struct {
	Providers []Declaration
	// Language is the ear's configured language, STT_LANGUAGE as written:
	// empty asks for whatever the server's model hears.
	Language string
}

// Finding is one need a provider in the pipeline does not meet.
type Finding struct {
	Need
	Provider string
	// Wanted is what was asked of a list capability, such as the language.
	Wanted string
	// Have is what the provider declares of it.
	Have []string
}

// String says which provider fell short, of what, and what it costs, as
// the startup log and error print it.
func (f Finding) String() string {
	what := fmt.Sprintf("%s provider %s lacks %s", f.Role, f.Provider, f.Capability)
	if f.Capability == Languages {
		what = fmt.Sprintf("%s provider %s hears %s, not %q", f.Role, f.Provider, strings.Join(f.Have, ", "), f.Wanted)
	}
	return fmt.Sprintf("%s: %s (%s)", what, f.Consequence, f.Source)
}

// Report is a check's findings by level, each in Needs' order.
type Report struct {
	Refused  []Finding
	Degraded []Finding
}

// Err is the refusals as one error naming every one, so one restart fixes
// them all, or nil when the pipeline can run.
func (r Report) Err() error {
	if len(r.Refused) == 0 {
		return nil
	}
	lines := make([]string, len(r.Refused))
	for i, f := range r.Refused {
		lines[i] = "  " + f.String()
	}
	return errors.New("the configured providers cannot run this pipeline (docs/reference/providers.md):\n" + strings.Join(lines, "\n"))
}

// Check holds each of the pipeline's providers to its role's needs.
func Check(p Pipeline) Report {
	var r Report
	for _, n := range Needs {
		for _, d := range p.Providers {
			if d.Role != n.Role {
				continue
			}
			f, met := meets(d, n, p.Language)
			if met {
				continue
			}
			if n.Level == Required {
				r.Refused = append(r.Refused, f)
			} else {
				r.Degraded = append(r.Degraded, f)
			}
		}
	}
	return r
}

// meets reports whether d meets n, and the finding when it does not.
func meets(d Declaration, n Need, language string) (Finding, bool) {
	f := Finding{Need: n, Provider: d.Name}
	if n.Capability != Languages {
		return f, d.Has(n.Capability)
	}
	f.Wanted, f.Have = language, d.Languages
	return f, d.Hears(language)
}

// Hears reports whether an ear hears language, compared by its primary
// subtag, so en-GB is English. Asking for nothing, or an ear that declares
// none, leaves it to the server's model.
func (c Capabilities) Hears(language string) bool {
	if language == "" || len(c.Languages) == 0 {
		return true
	}
	primary, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(language)), "-")
	primary, _, _ = strings.Cut(primary, "_")
	return slices.Contains(c.Languages, primary)
}
