# 0034. Curation verdicts live beside the journal, not in it

- **Status:** accepted
- **Source:** SPEC §8, §9.1, §9.2 · ADR-0026

The Curate screen needs somewhere to keep what a reviewer decided about a
harvested pair: accepted with which chosen side, discarded for which reason,
or edited and not yet accepted. The journal is the wrong place. SPEC §8 makes
the log the runtime's append-only record, folded by a reducer whose events
are schema-validated runtime facts; a verdict is a person's judgment about
that record, made later, and revised freely — a reviewer who changes their
mind must leave one current verdict, not a history the reducer has to fold.
So verdicts live in `internal/curation`: their own upserted Postgres table in
the same database, keyed by the harvester's pair id, with an in-memory store
keeping `task test` hermetic. Unreviewed is the absence of a row, which keeps
"no decision" one state, and the stored previous status is what lets Undo
work across requests on a stateless page.

```go
import "github.com/teaganglenn/chorus/internal/curation"

// A verdict names the pair it judges; unreviewed is the absence of one.
func verdictFor(d curation.Decision) string { return d.PairID }
```

The migration rides the journal's `schema_migrations` ledger under a
`curation/` namespace, so one table still tells the whole database's story.

## Alternatives rejected

- **Annotation events in the journal.** Honest about provenance, but it
  burdens every reader: the reducer, replay and the harvester would all need
  to know reviewer events are not runtime events (`#Actor` has no reviewer,
  deliberately), and "current verdict" becomes a fold over revisions that
  nothing else wants. ADR-0026 already settled the direction: the harvester
  derives candidates from the log and records nothing; curation is the step
  that records, and it records outside the log it reads.
- **Mutating the harvested row.** There is no harvested row. Candidates are
  derived on read (ADR-0026), so a verdict must reference, not replace them.

## Forecloses

A verdict keeps no history, so "who changed this and when" has only the
latest answer. If audit matters later, that is a new table of revisions, not
journal events.
