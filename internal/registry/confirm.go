package registry

import (
	"encoding/json"
	"slices"
	"strings"
)

// Classes reads the classes of what a call acts on, as its executor knows
// them: a Home Assistant cover's device_class, say (ADR-0041).
type Classes func() ([]string, error)

// NeedsConfirmation reports whether this call is held for the person's yes:
// every call to a tool that requires confirmation, and a call whose
// arguments match one of the tool's ConfirmWhen entries.
//
// Arguments that cannot be read are held: a gate that opens on a model's
// broken JSON is not a gate. So is a match on an entry that names target
// classes, since nothing here says what the call acts on.
func (t ToolSpec) NeedsConfirmation(args string) bool {
	return t.NeedsConfirmationOf(args, nil)
}

// NeedsConfirmationOf is NeedsConfirmation for a call whose target can be
// read. classes is asked only when the arguments match an entry that names
// target classes. A nil classes, or one that fails, holds the call.
func (t ToolSpec) NeedsConfirmationOf(args string, classes Classes) bool {
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
	target := targetOf(classes)
	for _, rule := range t.ConfirmWhen {
		if !matches(rule.Args, fields) {
			continue
		}
		if len(rule.TargetClass) == 0 {
			return true
		}
		got, ok := target()
		if !ok || slices.ContainsFunc(got, func(c string) bool { return holds(rule.TargetClass, c) }) {
			return true
		}
	}
	return false
}

// targetOf reads the target's classes at most once, however many entries
// ask. ok is false when they could not be read.
func targetOf(classes Classes) func() ([]string, bool) {
	var (
		read, ok bool
		got      []string
	)
	return func() ([]string, bool) {
		if !read && classes != nil {
			var err error
			got, err = classes()
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
