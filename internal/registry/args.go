package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// The arguments the orchestrator acts on rather than the tool. schemagen
// offers each where it applies, and no tool may declare either.
const (
	// ConfirmationParam carries a confirmed call's nonce. It is offered on
	// every confirmable tool (SPEC §6, ADR-0038).
	ConfirmationParam = "confirmation"

	// AcknowledgementParam is what to say while a slow tool works. It is
	// required on every slow tool (ADR-0039).
	AcknowledgementParam = "acknowledgement"
)

// Orchestrated is a call's arguments, split by who acts on them.
type Orchestrated struct {
	// Rest is what the tool is invoked with.
	Rest string
	// Nonce is the confirmation the call presented. Empty when none.
	Nonce string
	// Acknowledgement is what to say while the call works. Empty when none.
	Acknowledgement string
}

// Split separates a call's arguments into the ones the orchestrator acts on
// and the rest, which is what the tool is invoked with. The rest is
// re-encoded with sorted keys, so the same arguments compare equal however
// the model ordered them, and it carries neither the nonce nor the
// acknowledgement: a different "Unlocking it now." is still the same call.
func Split(args string) (Orchestrated, error) {
	fields := map[string]any{}
	if strings.TrimSpace(args) != "" {
		// Numbers stay as written: the rest is what the tool receives.
		d := json.NewDecoder(strings.NewReader(args))
		d.UseNumber()
		if err := d.Decode(&fields); err != nil {
			return Orchestrated{}, fmt.Errorf("arguments: %w", err)
		}
		if _, err := d.Token(); !errors.Is(err, io.EOF) {
			return Orchestrated{}, errors.New("arguments: more than one JSON value")
		}
	}
	var o Orchestrated
	for name, into := range map[string]*string{ConfirmationParam: &o.Nonce, AcknowledgementParam: &o.Acknowledgement} {
		v, ok := fields[name]
		if !ok {
			continue
		}
		s, isString := v.(string)
		if !isString && v != nil {
			return Orchestrated{}, fmt.Errorf("%s is %T, not a string", name, v)
		}
		*into = s
		delete(fields, name)
	}
	// Maps marshal with sorted keys at every depth, so the order the model
	// wrote them in does not count.
	b, err := json.Marshal(fields)
	if err != nil {
		return Orchestrated{}, fmt.Errorf("arguments: %w", err)
	}
	o.Rest = string(b)
	return o, nil
}
