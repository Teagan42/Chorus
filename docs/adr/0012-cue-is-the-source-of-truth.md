# 0012. Author invariants in CUE and commit the generated output

- **Status:** accepted
- **Source:** CONTRIBUTING §2 · commits `23cf628`, `b3a0400`, `3754b11`

`schema/*.cue` is the authoring layer for the event taxonomy, tool registry,
device profiles, provider capabilities, and config schema. One declaration
yields the model-facing JSON Schema, the orchestrator's Go types and policy
enforcement, and `docs/reference/*.md`, so the model's view and the enforced
policy cannot disagree. Policy fields are deliberately withheld from the model's
view (`b3a0400`).

CUE rather than JSON Schema because this project's invariants are cross-field
policy, not shapes:

```cue
// An uninterruptible tool must be confirmable, or a barge-in strands a side
// effect the user cannot stop.
#Tool: {
	on_interrupt: "cancel" | "detach" | "uninterruptible"
	requires_confirmation: bool
	if on_interrupt == "uninterruptible" {
		requires_confirmation: true
	}
}

uninterruptible: #Tool & {on_interrupt: "uninterruptible", requires_confirmation: true}
```

Generated output is committed (`3754b11`) so a clone builds without the
toolchain, the generator cannot silently rot, and a reviewer sees the real blast
radius of a schema change. `task gen:check` fails CI on a diff.

## Consequences

`cue vet` **must** carry `-c`. Plain `cue vet` passes on all four known-bad
fixtures in `schema/testdata/`, because only concrete evaluation reaches the
conditional policy constraints. CUE also ignores `testdata/` and any
`_`-prefixed file, so a probe named `_probe.cue` does nothing.

A schema change is therefore two commits — `feat(schema)` then `chore(gen)` —
per [ADR-0018](0018-mechanical-enforcement.md).
