package kokoro_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/kokoro"
)

// A barge-in cancels whatever the voice is doing; a declaration that it
// cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestKokoroIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := kokoro.New(kokoro.Config{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleTTS]["kokoro"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Synthesize(ctx, "The kitchen lights are off.")
		return err
	})
}
