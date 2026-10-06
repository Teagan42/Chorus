# Contributing

Five rules. Everything below is detail.

1. **Tests first.** Red, green, refactor. A bug gets a failing fixture before a fix.
2. **Schemas are the source of truth.** Types, policy, and docs are generated — never hand-edited.
3. **The spec is normative.** Tests cite the clause they verify. Untested clauses are visible.
4. **Commits are atomic.** Generated, dependency, formatting, and behavior changes never share a commit.
5. **Docs are build artifacts or they are executed.** Prose that can rot is either generated or run in CI.

`task check` is the gate. It runs locally in seconds and is identical in CI.

## Quick start

```sh norun
task tools          # install pinned toolchain into GOPATH/bin
task check          # lint + gen drift + tests + doc examples + spec trace
```

Requires Go 1.27+, `uv` for Python sidecars. `task` and `cue` install via `task tools`.

## 1. TDD

Three tiers, separated because two of them cannot run everywhere.

| Task | Needs | Runs |
|---|---|---|
| `task test` | nothing | every push, every commit, pre-commit hook |
| `task test:models` | GPU sidecars | self-hosted runner |
| `task test:hardware` | a real satellite | self-hosted runner, manual |

Hermetic means hermetic: no network, no wall clock, no GPU, no device. A test
that reaches the network is a bug in the test.

### The fake satellite is the primary test asset

Concurrency and interrupt timing are where this project's bugs live, and they
cannot be tested repeatably against hardware. `internal/esphome/fakedevice`
speaks the real protocol in-process — real Noise handshake, real framing — so
the whole stack runs under test with synthetic audio.

**Virtual clock, always.** Barge-in correctness is a question about
milliseconds; `time.Sleep` in a test makes the suite slow *and* flaky. Inject a
clock. Code that reads `time.Now()` directly is not testable and will be
rejected.

```go norun
// verifies SPEC §4.4
func TestBargeInKeepsArrivedToolResults(t *testing.T) {
    clk := clock.NewFake()
    // ...
}
```

### Hardware tests

Build-tagged `hardware`, skipped unless a satellite in `devices.yaml` answers.
They are smoke tests — "the real device still speaks what we think it speaks" —
not behavioral tests. Behavior is the fake's job.

## 2. Schema-driven everything

CUE is the authoring layer. It carries types *and constraints*, which is the
point: this project's invariants are cross-field policy, not shapes.

```
schema/*.cue  ──┬─→ internal/<pkg>/*.gen.go     Go types + validators
                ├─→ schema/json/*.json          JSON Schema (LLM tool schemas)
                └─→ docs/reference/*.md         reference tables
```

What is generated, in order of how badly drift hurts:

1. **Event taxonomy** → journal types, validator, replay exhaustiveness check, docs.
   An unvalidated event type silently corrupts training data. (SPEC §8)
2. **Tool registry** → Go interfaces, LLM-facing JSON Schema, policy enforcement
   middleware, docs. One declaration yields both the model's view and the
   interrupt/confirm/scope enforcement, so they cannot disagree. (SPEC §6)
3. **Device profiles** → capability matrix, `chorus_bridge` YAML, Go expectations. (SPEC §3)
4. **Provider capabilities** → build-time check that a configured pipeline is viable. (SPEC §10)
5. **Config schema** → validation with usable errors, plus reference docs. (SPEC §13)

Constraints CUE enforces that a type system cannot — these are why we chose it:

```cue
// A tool that cannot be interrupted must be confirmable, or a barge-in
// strands a side effect the user cannot stop.
#Tool: {
    on_interrupt: "cancel" | "detach" | "uninterruptible"
    if on_interrupt == "uninterruptible" { requires_confirmation: true }
}
```

### Generated files are committed

CI fails if `task gen` produces a diff (`task gen:check`). Two reasons: the
generator cannot silently rot, and a reviewer sees the actual blast radius of a
schema change. Generated files carry a header and live at `*.gen.go` or under
`schema/json/`.

Never hand-edit a generated file. Change the schema.

## 3. The spec is normative

`docs/SPEC.md` sections are numbered and stable. Tests cite clauses:

```go norun
// verifies SPEC §4.3
```

`task spec` reports the mapping both ways:

- **Uncovered clauses** — documented behavior nothing verifies.
- **Orphan citations** — a test citing a clause that no longer exists, which
  means the spec moved and the test's intent is now unclear.

`task spec:check` fails CI on orphan citations. Uncovered clauses are reported
but do not fail the build — some clauses are rationale, not behavior.

Clauses listed in SPEC §15 ("cannot be retrofitted") **must** have coverage.
That list is the one place where missing tests fail the build.

### ADRs

Architectural decisions live in `docs/adr/`, numbered, never edited after
acceptance — superseded instead. If a decision needed a paragraph of
justification, it needs an ADR. Use `docs/adr/0000-template.md`.

### Executable docs

Fenced code blocks tagged `go`, `sh`, or `cue` in `docs/` and `CONTRIBUTING.md`
are extracted and run by `task test:docs`. A stale example fails the build.

Opt a block out with `norun` when it is illustrative rather than runnable:

````
```go norun
// a sketch, not a compilable example
```
````

Use `norun` sparingly. It is the right call for command listings, snippets
citing packages that do not exist yet, and anything with side effects — but a
`norun` block is unverified prose, so prefer rewriting the example to be real.

A `go` block needs no `package` clause or `func main`; fragments are
type-checked as a library against this module. So an example citing a real
symbol fails the build when that symbol is renamed:

```go
import "github.com/teaganglenn/chorus/internal/registry"

// media_search is declared slow, so the model may speak before results land.
func mediaSearchIsSlow() bool { return registry.Specs["media_search"].Slow }
```

That block is not decoration. `registry.Specs` is generated from
`schema/tool.cue`, so renaming `Slow` or dropping the `media_search` tool
breaks this example and `task test:docs` fails.

Note the limit: `go` blocks are type-checked, not executed, so they catch
renamed and removed symbols — not changed values. Assert behavior in a real
test, not in prose.

## 4. Atomic commits

Conventional Commits, enforced mechanically — because convention alone does not
prevent the failure that actually hurts: a 400-line diff where 380 lines are
regenerated and 20 change behavior.

```
<type>(<scope>): <subject>
```

Types: `feat` `fix` `refactor` `perf` `test` `docs` `style` `chore` `build` `ci`.

`task commit:check` enforces path rules on `HEAD`, or a range with
`task commit:check -- origin/main..HEAD`:

| Type | May touch | May not |
|---|---|---|
| `chore(gen)` | generated paths only | anything hand-written |
| `chore(deps)` | manifests, lockfiles, `vendor/` | logic |
| `style` | anything, but must be a formatting no-op | semantics |
| everything else | hand-written source | generated paths, lockfiles |

So a schema change is two commits: `feat(schema): …` for the CUE, then
`chore(gen): …` for the regenerated output. This is deliberate. The second
commit is reviewable at a glance precisely because the first one isn't mixed
into it.

Install the hooks once:

```sh norun
git config core.hooksPath .githooks
```

## 5. Project layout

```
cmd/                    binaries (probe, orchestrator, reviewui)
internal/
  esphome/              native API client, Noise transport
    fakedevice/         in-process satellite for tests
  pb/                   generated ESPHome bindings (do not edit)
  session/              actor/supervisor model (SPEC §4)
  journal/              append-only event log, replay (SPEC §8)
  registry/             tool registry and policy (SPEC §6)
  provider/             STT/LLM/TTS/speaker-ID interfaces (SPEC §10)
  config/               inventory and settings
  tools/                build-time tooling (schemagen, spectrace, atomic, docexec)
schema/                 CUE source of truth
  json/                 generated JSON Schema (do not edit)
sidecars/               Python model services (uv workspace)
esphome/                chorus_bridge external component + YAML packages
docs/
  SPEC.md               normative spec
  adr/                  architecture decision records
  reference/            generated reference docs (do not edit)
proto/esphome/          vendored ESPHome protos
```

Go code is `internal/` by default. Promote to a public package only when
something outside this repo needs it.

## 6. Code conventions

- Comments explain *why*, never *what*. Hard cap: 15 words inline, 30 words on
  a doc block. If naming or structure removes the need for a comment, do that
  instead.
- Errors wrap with context and name the thing that failed:
  `fmt.Errorf("dial %s: %w", addr, err)`.
- No package-level mutable state outside generated registries.
- Concurrency: every goroutine takes a `context.Context` and exits on cancel.
  The supervisor owns lifetimes (SPEC §4) — goroutines that outlive their parent
  are the bug class this architecture exists to avoid.
- Secrets never enter the repo. PSKs live in gitignored `devices.yaml`.

## 7. Audio and privacy

Captured audio is training data and it is also a recording of someone's home.

- Journal and blob retention is configurable per satellite (SPEC §8).
- Hardware mute is authoritative. Never override it.
- Test fixtures use synthetic or explicitly-consented audio. Never commit a
  real household recording.
