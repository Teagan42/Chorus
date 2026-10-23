package registry_test

import (
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/registry"
)

// No declared tool takes the prefix a provided tool's name opens with, so a
// name that opens with it was the house's.
//
// verifies SPEC §6
func TestNoDeclaredToolTakesTheProvidedPrefix(t *testing.T) {
	for name := range registry.Specs {
		if strings.HasPrefix(name, registry.ProvidedPrefix) {
			t.Errorf("declared tool %s takes %s, which names a server's tool", name, registry.ProvidedPrefix)
		}
	}
}

// Teagan asked the kitchen to put oat milk on the shopping list, which the
// notes server offered. A re-run under a later build is offered that
// build's tools and the notes server's tool, as the turn was offered it.
// A declared tool the turn's build had and this one dropped stays dropped:
// the build's tools are the build's to change.
//
// verifies SPEC §6, §9.2
func TestARerunCarriesTheHousesToolsTheTurnWasOffered(t *testing.T) {
	shopping, err := registry.Provide(registry.ToolSpec{
		Name: "mcp_notes_add_to_shopping_list", Description: "Add an item to the household shopping list.",
		Params:      []registry.ParamSpec{{Name: "item", Type: "string", Description: "What to buy.", Required: true}},
		OnInterrupt: registry.InterruptCancel, Scope: registry.ScopePerson, UnknownSpeaker: "deny",
		Timeout: 5 * time.Second, RequiresConfirmation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorded := registry.Offered()
	recorded[shopping.Name] = shopping
	recorded["media_search"] = registry.Specs["media_search"]
	offered := registry.Offered()
	delete(offered, "timer_cancel")

	got := registry.WithProvided(offered, recorded)
	if _, ok := got[shopping.Name]; !ok {
		t.Error("the notes server's tool was cut")
	}
	if _, ok := got["media_search"]; ok {
		t.Error("a declared tool this build defers came back from the recording")
	}
	if _, ok := got["timer_cancel"]; ok {
		t.Error("a declared tool this build left out came back from the recording")
	}
	if _, ok := offered[shopping.Name]; ok {
		t.Error("offered was changed")
	}
	if len(got) != len(offered)+1 {
		t.Errorf("got %d tools, want the build's %d and the shopping list", len(got), len(offered))
	}
}
