package registry

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"
)

// The model-facing parts schemagen adds to a declared tool. Spelled here as
// internal/tools/schemagen spells them; TestAProvidedToolIsOfferedAsAGeneratedOneIs
// keeps the two in step.
const (
	slowHint        = " Takes several seconds to return."
	confirmAllHint  = " Every call needs the person's yes: it returns confirmation_required with a nonce. Ask them, and once they agree, call again with the same arguments and confirmation set to the nonce."
	confirmSomeHint = " Some calls need the person's yes: they return confirmation_required with a nonce. Ask them, and once they agree, call again with the same arguments and confirmation set to the nonce."
)

var (
	ackParam = ParamSpec{
		Name:        AcknowledgementParam,
		Type:        "string",
		Description: "A few words said aloud while this runs, e.g. \"Searching the library.\" They are spoken for you as it starts; do not also call speak for them.",
		Required:    true,
	}
	confirmParam = ParamSpec{
		Name:        ConfirmationParam,
		Type:        "string",
		Description: "The nonce from a confirmation_required result, once the person has said yes. Omit it otherwise.",
	}
)

var (
	// toolName is tool.cue's #Tool.name.
	toolName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// providedParam is looser than tool.cue's #Param.name: a provided tool's
	// arguments reach its server as written, so they keep the server's names.
	providedParam = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,63}$`)
	jsonTypes     = []string{"string", "number", "integer", "boolean", "array", "object"}
)

// Bounds tool.cue puts on timeout_ms, and the floor it puts on a slow tool's.
const (
	minTimeout  = 100 * time.Millisecond
	maxTimeout  = 120 * time.Second
	slowTimeout = 10 * time.Second
)

// Provide finishes a tool a provider declares at runtime, such as one an MCP
// server lists: the model's view is derived as schemagen derives a declared
// tool's, after checking the declaration against tool.cue's rules, so a
// provided tool is held to the same policy as a generated one (SPEC §6).
// ModelDescription and ModelParams are ignored and rewritten.
func Provide(spec ToolSpec) (ToolSpec, error) {
	if err := check(spec); err != nil {
		return ToolSpec{}, fmt.Errorf("tool %q: %w", spec.Name, err)
	}
	spec.ModelDescription = describe(spec)
	spec.ModelParams = offered(spec)
	return spec, nil
}

// check is tool.cue's #Tool, for a declaration CUE never saw.
func check(t ToolSpec) error {
	if !toolName.MatchString(t.Name) {
		return errors.New("name must match ^[a-z][a-z0-9_]*$")
	}
	if t.Description == "" {
		return errors.New("description is empty")
	}
	seen := map[string]bool{}
	for _, p := range t.Params {
		if err := checkParam(p); err != nil {
			return fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		if seen[p.Name] {
			return fmt.Errorf("parameter %q is declared twice", p.Name)
		}
		seen[p.Name] = true
	}
	if t.Timeout < minTimeout || t.Timeout > maxTimeout {
		return fmt.Errorf("timeout %v is outside %v to %v", t.Timeout, minTimeout, maxTimeout)
	}
	switch t.OnInterrupt {
	case InterruptCancel, InterruptDetach:
	case InterruptUninterruptible:
		// A barge-in would strand a side effect the person cannot stop.
		if !t.RequiresConfirmation {
			return errors.New("an uninterruptible tool must require confirmation")
		}
	default:
		return fmt.Errorf("on_interrupt %q is not cancel, detach or uninterruptible", t.OnInterrupt)
	}
	switch t.Scope {
	case ScopePerson:
		// Or an unknown voice silently inherits someone's permissions.
		if t.UnknownSpeaker != "deny" && t.UnknownSpeaker != "guest_fallback" {
			return errors.New("a person-scoped tool must set unknown_speaker to deny or guest_fallback")
		}
	case ScopeHousehold, ScopeGuest:
		if t.UnknownSpeaker != "" {
			return fmt.Errorf("unknown_speaker applies only to a person-scoped tool, not a %s one", t.Scope)
		}
	default:
		return fmt.Errorf("scope %q is not household, person or guest", t.Scope)
	}
	if t.Slow && t.Timeout < slowTimeout {
		return fmt.Errorf("a slow tool's timeout must be at least %v, not %v", slowTimeout, t.Timeout)
	}
	for i, rule := range t.ConfirmWhen {
		if len(rule.Args) == 0 && len(rule.TargetClass) == 0 && len(rule.TargetDomain) == 0 {
			return fmt.Errorf("confirm_when entry %d names nothing to match", i+1)
		}
	}
	return nil
}

func checkParam(p ParamSpec) error {
	switch {
	case p.Name == ConfirmationParam, p.Name == AcknowledgementParam:
		return errors.New("the name is the orchestrator's")
	case !providedParam.MatchString(p.Name):
		return errors.New("name must be letters, digits, _ . or -, starting with a letter or _")
	case !slices.Contains(jsonTypes, p.Type):
		return fmt.Errorf("type %q is not a JSON Schema type", p.Type)
	case p.Description == "":
		return errors.New("description is empty")
	case p.Type == "array" && !slices.Contains(jsonTypes, p.Items):
		return fmt.Errorf("an array's items type %q is not a JSON Schema type", p.Items)
	}
	return nil
}

// offered is schemagen's offered: the declared parameters, the
// acknowledgement on a slow tool and the nonce on a confirmable one.
func offered(t ToolSpec) []ParamSpec {
	out := append([]ParamSpec(nil), t.Params...)
	if t.Slow {
		out = append(out, ackParam)
	}
	if t.RequiresConfirmation || len(t.ConfirmWhen) > 0 {
		out = append(out, confirmParam)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// describe is schemagen's describe: latency and confirmation are policy the
// model has to act on.
func describe(t ToolSpec) string {
	d := t.Description
	if t.Slow {
		d += slowHint
	}
	switch {
	case t.RequiresConfirmation:
		d += confirmAllHint
	case len(t.ConfirmWhen) > 0:
		d += confirmSomeHint
	}
	return d
}
