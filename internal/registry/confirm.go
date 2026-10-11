package registry

import (
	"encoding/json"
	"slices"
	"strings"
)

// Classes reads the classes of what a call acts on, as its executor knows
// them: a Home Assistant cover's device_class, say (ADR-0041).
type Classes func() ([]string, error)

// Target is what a call acts on, as its executor reads it: the domain of
// the entity the call names, and the classes Home Assistant gives it
// (ADR-0041, ADR-0063). An empty Domain is one nothing read.
type Target struct {
	Domain  string
	Classes []string
}

// TargetReader reads a call's target.
type TargetReader func() (Target, error)

// NeedsConfirmation reports whether this call is held for the person's yes:
// every call to a tool that requires confirmation, and a call whose
// arguments match one of the tool's ConfirmWhen entries.
//
// Arguments that cannot be read are held: a gate that opens on a model's
// broken JSON is not a gate. So is a match on an entry that names target
// classes or domains, since nothing here says what the call acts on.
func (t ToolSpec) NeedsConfirmation(args string) bool {
	return t.NeedsConfirmationFor(args, nil)
}

// NeedsConfirmationOf is NeedsConfirmationFor with a reader of the target's
// classes alone. An entry that names target domains holds the call, since
// such a reader cannot say the domain.
func (t ToolSpec) NeedsConfirmationOf(args string, classes Classes) bool {
	if classes == nil {
		return t.NeedsConfirmationFor(args, nil)
	}
	return t.NeedsConfirmationFor(args, func() (Target, error) {
		got, err := classes()
		return Target{Classes: got}, err
	})
}

// NeedsConfirmationFor is NeedsConfirmation for a call whose target can be
// read. target is asked only when the arguments match an entry that names
// target classes or domains, and at most once. A nil target, one that
// fails, or one that leaves the domain unread when an entry names domains,
// holds the call.
func (t ToolSpec) NeedsConfirmationFor(args string, target TargetReader) bool {
	if t.RequiresConfirmation {
		return true
	}
	if len(t.ConfirmWhen) == 0 {
		return false
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(args), &fields); err != nil {
		return true
	}
	read := targetOf(target)
	for _, rule := range t.ConfirmWhen {
		if !matches(rule.Args, fields) {
			continue
		}
		if len(rule.TargetClass) == 0 && len(rule.TargetDomain) == 0 {
			return true
		}
		got, ok := read()
		if !ok {
			return true
		}
		// Every named constraint must hold; a domain nothing read is held.
		if len(rule.TargetDomain) > 0 && got.Domain != "" && !holds(rule.TargetDomain, got.Domain) {
			continue
		}
		if len(rule.TargetClass) > 0 && !slices.ContainsFunc(got.Classes, func(c string) bool { return holds(rule.TargetClass, c) }) {
			continue
		}
		return true
	}
	return false
}

// targetOf reads the target at most once, however many entries ask. ok is
// false when it could not be read.
func targetOf(target TargetReader) func() (Target, bool) {
	var (
		read, ok bool
		got      Target
	)
	return func() (Target, bool) {
		if !read && target != nil {
			var err error
			got, err = target()
			ok = err == nil
		}
		read = true
		return got, ok
	}
}

// holds reports whether class is one of an entry's, ignoring case and
// stray space as matches does.
func holds(want []string, class string) bool {
	return slices.ContainsFunc(want, func(w string) bool {
		return strings.EqualFold(strings.TrimSpace(class), w)
	})
}

// matches reports whether every parameter an entry names has its value. Case
// is ignored: a backend that folds case, as Home Assistant's service registry
// does, would otherwise run "LOCK.UNLOCK" unconfirmed.
func matches(when map[string]string, fields map[string]any) bool {
	for name, want := range when {
		got, ok := fields[name].(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(got), want) {
			return false
		}
	}
	return true
}
