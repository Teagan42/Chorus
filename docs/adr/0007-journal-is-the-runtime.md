# 0007. Make the event journal the source of truth, not instrumentation

- **Status:** accepted · unretrofittable (SPEC §15.2, §15.5)
- **Source:** SPEC §8, §11

The orchestrator is a pure reducer over an append-only event log. Tracing is not
instrumentation; it is the runtime. With that invariant held, the review UI,
replay harness, and dataset export are readers of existing data instead of new
plumbing. Storage is Postgres `JSONB` partitioned by conversation, plus
MinIO/filesystem for audio blobs; ~115 MB/day of continuous 16 kHz mono capture,
retention configurable per satellite.

Deterministic replay requires recording every nondeterministic input: model
completions (not only requests), tool results, and barge-in timing to the
millisecond relative to TTS position. Every event carries a monotonic sequence
number, wall clock, and the model, prompt, and tool-schema versions in effect.
`replay(conversation_id, overrides)` is tested from week one — it is what keeps
the reducer honest.

The speculative/non-speculative distinction is present in the log from the
start even though phase 1 generates no speculative work. Adding an event type
later is cheap; retrofitting the distinction into replay is not (§11).

```go
import "github.com/teagan42/chorus/internal/journal"

// Replay asserts exhaustive handling against the generated kind list.
func everyKindIsKnown() int { return len(journal.AllKinds) }
```

## Consequences

OpenTelemetry is emitted alongside, so Langfuse and Jaeger give exploration for
free without the annotation layer leaving this repo (see
[ADR-0019](0019-own-the-annotation-layer.md)).
