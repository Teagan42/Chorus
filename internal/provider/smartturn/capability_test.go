package smartturn_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/teagan42/chorus/internal/provider/capability"
	"github.com/teagan42/chorus/internal/provider/capability/capabilitytest"
	"github.com/teagan42/chorus/internal/provider/smartturn"
)

// alanInTheKitchen is a tenth of a second of quiet device audio: enough to
// be a request, which is all a hung server needs to hold.
var alanInTheKitchen = make([]byte, 3200)

// A barge-in cancels whatever the endpointer is doing; a declaration that
// it cancels is proved against a server that never answers (ADR-0102).
//
// verifies SPEC §10
func TestSmartTurnIsHeldToItsDeclaration(t *testing.T) {
	url, arrived := capabilitytest.Hung(t)
	p, err := smartturn.New(smartturn.Config{BaseURL: url})
	if err != nil {
		t.Fatal(err)
	}
	d := p.Capabilities()
	if want := capability.Declared[capability.RoleEndpointing]["smartturn"]; !reflect.DeepEqual(d, want) {
		t.Fatalf("Capabilities() = %+v, want schema/provider.cue's %+v", d, want)
	}
	capabilitytest.Cancels(t, d, arrived, func(ctx context.Context) error {
		_, err := p.Complete(ctx, alanInTheKitchen)
		return err
	})
}
