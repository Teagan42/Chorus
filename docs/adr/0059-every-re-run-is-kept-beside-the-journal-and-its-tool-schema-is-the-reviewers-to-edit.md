# 0059. Every re-run is kept beside the journal, and its tool schema is the reviewer's to edit

- **Status:** accepted
- **Source:** SPEC §8, §9.2 · ADR-0052, ADR-0054

SPEC §9.2's edit-and-replay changes the prompt *or tool schema*, but Replay
let a reviewer edit only the prompt and model, and ADR-0052 kept only the
take a reviewer promoted: leaving Replay lost every other re-run. Now the
declarations the model is offered are edited beside the prompt, and every
re-run that reaches a turn is kept in `curation_reruns`, beside the journal
for ADR-0052's reason: a re-run is a reviewer's doing, not the runtime's.

```go
import "github.com/teagan42/chorus/internal/provider/ollama"

// What Replay's editor holds, and what it reads back before a re-run.
var _ = ollama.ParseToolSchema
```

**The tool schema is edited in the wire's shape.** Replay shows
`ollama.ToolSchema`, the declarations exactly as `/api/chat` is sent them,
and `ollama.ParseToolSchema` reads an edit back into the specs an engine is
built from. It refuses what the wire cannot carry, such as an unknown field,
a type JSON Schema does not have, or a required parameter nobody declared,
saying where, rather than dropping it: the tool-schema version is a hash of
what was sent (SPEC §8), so a quietly dropped field would make it name a
schema nobody wrote. An unedited schema reads back to the registry's own
version, so a re-run that changes only the prompt stays comparable on
tools. The edit is diffed against the registry's schema, as the prompt is
against the default, trimmed to the lines near each change.

**A re-run is kept whole and never replaced.** Each row holds the versions
the server computed when it built the engine, the edited prompt and tool
declarations themselves, and what each turn said and called. A run the
model stopped keeps the turns before it; one that reached no turn keeps
nothing, since nothing in it can be compared or promoted. Replay lists a
conversation's runs newest first, and each opens at
`/replays/{id}/runs/{run}` with its comparison and the editor holding what
it ran under.

**A promotion names a kept take.** Promote posts the run and turn, nothing
else; the take, its versions, prompt and tool declarations come from the
row, where ADR-0052 recomputed the versions from the model and prompt the
page sent back. So no take reaches a pair on the page's word, a take that
answered as recorded is refused, and a run is promotable later from a box
with no model to ask. A promotion now keeps the tool declarations beside
the prompt; rows promoted before keep only the version.

## Alternatives rejected

- **Keep only promoted takes** (ADR-0052). A model's answer is not
  reproducible on demand: the endpoint changes, a sampled answer differs.
  The run a reviewer meant to come back to is the one that is gone.
- **Store re-runs in the journal.** The journal records what the runtime
  did (SPEC §8); a reviewer's what-if is not that, and appending it would
  make every replay of the log carry them.
- **Edit tools as toggles and description fields.** It covers dropping a
  tool and rewording one, but not a parameter's description, enum or
  whether it is required, which are the edits a wrong-args turn calls for.

## Forecloses

Kept re-runs grow without bound; nothing prunes them yet. Only what the
wire's shape carries can be edited: a parameter's own schema beyond type,
description, enum and an array's item type cannot be tried without
widening `ollama.Config.Specs` first.
