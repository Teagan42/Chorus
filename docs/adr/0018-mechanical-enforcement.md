# 0018. Enforce the five rules with tools, not review

- **Status:** accepted
- **Source:** CONTRIBUTING §1–§5 · commits `a954e59`, `ef948f6`, `85b4dc0`

Four build-time guards exist because convention alone does not prevent the
failures that actually hurt on a one-person project:

| Guard | Prevents |
|---|---|
| `schemagen` + `gen:check` | A generated file drifting from its CUE source. |
| `spectrace` | A documented behavior nothing verifies, and a test citing a clause the spec no longer has. |
| `atomic` | The 400-line diff where 380 lines are regenerated and 20 change behavior (`a954e59`). |
| `docexec` | Prose citing a symbol that was renamed or removed (`85b4dc0`). |

`spectrace` scans **test files only** and **Go comments only**, via `go/ast`, by
design. Orphan citations fail the build; uncovered clauses are informational
except those SPEC §15 lists as unretrofittable, which must have coverage.

Go blocks in markdown are **type-checked, not executed** (`85b4dc0`). They catch
renamed and removed symbols, not changed values. Assert behavior in a test, not
in prose.

## Consequences

`task spec:check` is deliberately red while the phase-1 runtime is unwritten —
five §15 clauses have no coverage because the code does not exist. That list is
the work order, not a broken gate. Do not weaken `spectrace` or delete a
citation to get green.
