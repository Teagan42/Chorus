# 0011. Keep tool policy in a native registry; MCP is one provider

- **Status:** accepted · unretrofittable (SPEC §15.6)
- **Source:** SPEC §6, §4.4 · commit `23cf628`

The tool registry is native Go. MCP is one provider among several and Home
Assistant is an adapter exposing entities, areas, and scripts. The registry owns
what MCP has no vocabulary for: `on_interrupt` policy, `timeout`, person
scoping, `requires_confirmation`, and latency hints. MCP-sourced tools get
conservative defaults.

`on_interrupt` is per-tool and unretrofittable: `cancel` (default), `detach`
(finish and keep the otherwise wasted result), `uninterruptible` (side effects
already committed, so it must complete).

```go
import "github.com/teagan42/chorus/internal/registry"

// Declared-slow tools let the model speak before results land.
func slowToolDetaches() bool {
	spec := registry.Specs["media_search"]
	return spec.Slow && spec.OnInterrupt == registry.InterruptDetach
}
```

## Consequences

Confirmation is orchestrator-enforced but model-authored: execution blocks and a
synthetic `confirmation_required` result carrying a nonce goes back to the
model, which phrases the confirmation itself and re-calls with the nonce. That
gives a hard safety guarantee without the orchestrator owning the dialogue, and
the nonce makes "did the user actually say yes" auditable in the journal.
