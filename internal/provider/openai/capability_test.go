package openai_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/openai"
)

// alanInTheKitchen is a tenth of a second of quiet device audio: enough to
// be a request, which is all a hung server needs to hold.
var alanInTheKitchen = make([]byte, 3200)

// A barge-in cancels whatever the ear is doing; a declaration that it
// cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestTheOpenAIEarIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := openai.NewTranscriber(openai.TranscriberConfig{BaseURL: url + "/v1", Model: "whisper-1"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleSTT]["openai"]; !reflect.DeepEqual(d, want) {
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
func TestTheOpenAIVoiceIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := openai.NewSynth(openai.SynthConfig{BaseURL: url + "/v1", Model: "tts-1", Voice: "nova"})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleTTS]["openai"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Synthesize(ctx, "The kitchen lights are off.")
		return err
	})
}
