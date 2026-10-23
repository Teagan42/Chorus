package wyoming_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/wyoming"
)

// alanInTheKitchen is a tenth of a second of quiet device audio: enough to
// be a request, which is all a hung server needs to hold.
var alanInTheKitchen = make([]byte, 3200)

// A barge-in cancels whatever the ear is doing; a declaration that it
// cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestTheWyomingEarIsHeldToItsDeclaration(t *testing.T) {
	addr, arrived := capabilitytest.HungTCP(t)
	p, err := wyoming.NewTranscriber(wyoming.TranscriberConfig{Address: addr, Model: "faster-whisper/small-int8"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleSTT]["wyoming"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Transcribe(ctx, alanInTheKitchen)
		return err
	})
}

// A barge-in cancels whatever the voice is doing; a declaration that it
// cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestTheWyomingVoiceIsHeldToItsDeclaration(t *testing.T) {
	addr, arrived := capabilitytest.HungTCP(t)
	p, err := wyoming.NewSynth(wyoming.SynthConfig{Address: addr, Voice: "en_GB-alba-medium"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleTTS]["wyoming"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Synthesize(ctx, "The kitchen lights are off.")
		return err
	})
}
