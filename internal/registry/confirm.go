package registry

import (
	"encoding/json"
	"strings"
)

// NeedsConfirmation reports whether this call is held for the person's yes:
// every call to a tool that requires confirmation, and a call whose
// arguments match one of the tool's ConfirmWhen entries.
//
// Arguments that cannot be read are held: a gate that opens on a model's
// broken JSON is not a gate.
func (t ToolSpec) NeedsConfirmation(args string) bool {
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
	for _, when := range t.ConfirmWhen {
		if matches(when, fields) {
			return true
		}
	}
	return false
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
