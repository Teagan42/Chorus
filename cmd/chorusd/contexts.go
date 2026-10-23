package main

import (
	"context"
	"fmt"

	"github.com/teagan42/chorus/internal/contexts"
)

// recordOffer keeps the text behind the versions every event this process
// journals will carry, so an export, an eval and a re-run can resolve them
// after the process is gone: the house's MCP tools make a schema only this
// process can name (ADR-0101). An offer that does not hash to the engine's
// labels is a bug, and recording it would file text under a label that
// never named it.
func recordOffer(ctx context.Context, store contexts.Store, p providers) error {
	prompt, tools, err := p.offer.Versions()
	if err != nil {
		return err
	}
	if prompt != p.versions.Prompt || tools != p.versions.ToolSchema {
		return fmt.Errorf("the engine records prompt %s and tools %s, but what it is offered hashes to %s and %s",
			p.versions.Prompt, p.versions.ToolSchema, prompt, tools)
	}
	return store.Record(ctx, p.offer)
}
