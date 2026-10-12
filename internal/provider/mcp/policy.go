package mcp

import (
	"fmt"
	"time"

	"github.com/teagan42/chorus/internal/registry"
)

// The policy every MCP tool runs under unless the household's configuration
// says otherwise (SPEC §6, ADR-0075). A server is code chorusd did not write,
// acting on whatever its credentials reach, so each default is the one that
// does least when it is wrong.
const (
	// DefaultScope runs a tool for identified people only: not a guest, and
	// not the television.
	DefaultScope = registry.ScopePerson
	// DefaultUnknownSpeaker refuses an unidentified voice outright.
	DefaultUnknownSpeaker = "deny"
	// DefaultRequiresConfirmation holds every call for the asker's yes.
	DefaultRequiresConfirmation = true
	// DefaultOnInterrupt cancels the call on a barge-in or its timeout, and
	// tells the server so: nothing chorusd started is left running.
	DefaultOnInterrupt = registry.InterruptCancel
	// DefaultTimeout is half the registry's: a voice turn that waits longer
	// on a server it knows nothing about is a silent house.
	DefaultTimeout = 5 * time.Second
	// DefaultSlow is false: a slow tool asks the model for an acknowledgement
	// on every call, which only the household can say a tool deserves.
	DefaultSlow = false
)

// Policy is what the registry owns and MCP has no word for (SPEC §6), as
// the configuration sets it. A nil field inherits; see DefaultPolicy.
type Policy struct {
	// Offer false leaves a tool unoffered: the model never sees it.
	Offer *bool `yaml:"offer"`

	Scope                *registry.Scope           `yaml:"scope"`
	UnknownSpeaker       *string                   `yaml:"unknown_speaker"`
	RequiresConfirmation *bool                     `yaml:"requires_confirmation"`
	OnInterrupt          *registry.InterruptPolicy `yaml:"on_interrupt"`
	TimeoutMS            *int                      `yaml:"timeout_ms"`
	// Latency is fast or slow, as tool.cue's latency is.
	Latency *string `yaml:"latency"`
}

// DefaultPolicy is every default above, offered.
func DefaultPolicy() Policy {
	offer, confirm := true, DefaultRequiresConfirmation
	scope, unknown, interrupt := DefaultScope, DefaultUnknownSpeaker, DefaultOnInterrupt
	timeout, latency := int(DefaultTimeout/time.Millisecond), "fast"
	return Policy{
		Offer: &offer, Scope: &scope, UnknownSpeaker: &unknown, RequiresConfirmation: &confirm,
		OnInterrupt: &interrupt, TimeoutMS: &timeout, Latency: &latency,
	}
}

// over is p with every field top sets replaced by top's. Relaxing the scope
// drops an inherited unknown_speaker, which only a person-scoped tool has.
func (p Policy) over(top Policy) Policy {
	if top.Scope != nil && *top.Scope != registry.ScopePerson && top.UnknownSpeaker == nil {
		p.UnknownSpeaker = nil
	}
	if top.Scope != nil && *top.Scope == registry.ScopePerson && p.UnknownSpeaker == nil && top.UnknownSpeaker == nil {
		unknown := DefaultUnknownSpeaker
		p.UnknownSpeaker = &unknown
	}
	p.Offer = or(top.Offer, p.Offer)
	p.Scope = or(top.Scope, p.Scope)
	p.UnknownSpeaker = or(top.UnknownSpeaker, p.UnknownSpeaker)
	p.RequiresConfirmation = or(top.RequiresConfirmation, p.RequiresConfirmation)
	p.OnInterrupt = or(top.OnInterrupt, p.OnInterrupt)
	p.TimeoutMS = or(top.TimeoutMS, p.TimeoutMS)
	p.Latency = or(top.Latency, p.Latency)
	return p
}

// or is set when it is set, else inherited.
func or[T any](set, inherited *T) *T {
	if set != nil {
		return set
	}
	return inherited
}

// check refuses a value no tool could take; how the fields combine is the
// registry's to check (registry.Provide).
func (p Policy) check() error {
	if p.Latency != nil && *p.Latency != "fast" && *p.Latency != "slow" {
		return fmt.Errorf("latency %q is not fast or slow", *p.Latency)
	}
	return nil
}

// offered reports whether the resolved policy offers the tool.
func (p Policy) offered() bool { return p.Offer == nil || *p.Offer }

// apply sets spec's policy from p, which DefaultPolicy has filled.
func (p Policy) apply(spec registry.ToolSpec) registry.ToolSpec {
	spec.Scope = *p.Scope
	spec.UnknownSpeaker = ""
	if p.UnknownSpeaker != nil {
		spec.UnknownSpeaker = *p.UnknownSpeaker
	}
	spec.RequiresConfirmation = *p.RequiresConfirmation
	spec.OnInterrupt = *p.OnInterrupt
	spec.Timeout = time.Duration(*p.TimeoutMS) * time.Millisecond
	spec.Slow = *p.Latency == "slow"
	return spec
}
