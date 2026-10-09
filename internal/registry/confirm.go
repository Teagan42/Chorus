package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ConfirmationParam is the argument a confirmed call carries its nonce in.
// schemagen offers it on every confirmable tool (SPEC §6, ADR-0038).
const ConfirmationParam = "confirmation"

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

// Unconfirmed splits a call's arguments into its nonce and everything else.
// The rest is re-encoded with sorted keys, so the same arguments compare
// equal however the model ordered them, and is what the tool is invoked with:
// the nonce is the orchestrator's, not the tool's.
func Unconfirmed(args string) (rest, nonce string, err error) {
	fields := map[string]any{}
	if strings.TrimSpace(args) != "" {
		// Numbers stay as written: the rest is what the tool receives.
		d := json.NewDecoder(strings.NewReader(args))
		d.UseNumber()
		if err := d.Decode(&fields); err != nil {
			return "", "", fmt.Errorf("arguments: %w", err)
		}
		if _, err := d.Token(); !errors.Is(err, io.EOF) {
			return "", "", errors.New("arguments: more than one JSON value")
		}
	}
	if v, ok := fields[ConfirmationParam]; ok {
		s, isString := v.(string)
		if !isString && v != nil {
			return "", "", fmt.Errorf("%s is %T, not a string", ConfirmationParam, v)
		}
		nonce = s
		delete(fields, ConfirmationParam)
	}
	// Maps marshal with sorted keys at every depth, so the order the model
	// wrote them in does not count.
	b, err := json.Marshal(fields)
	if err != nil {
		return "", "", fmt.Errorf("arguments: %w", err)
	}
	return string(b), nonce, nil
}
