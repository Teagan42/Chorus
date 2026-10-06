# 0002. Model a session as a supervisor, not a pipeline

- **Status:** accepted
- **Source:** SPEC §4

A session is a supervisor owning concurrent children — `Listening`, `Thinking`,
`Speaking`, and one per in-flight tool call — that start, finish, and cancel
independently. There is no stage enum: the lifecycle is whichever children are
alive. Goroutines plus `context.Context` cancellation map onto this directly.

This is the load-bearing choice. Assist's rigidity comes from interruption
being an exceptional path; here it is ordinary cancellation, so speak-while-
tooling, barge-in with retained context, multi-turn interleaving, and proactive
speech are all structural rather than special cases (§4 table).

## Alternatives rejected

A staged pipeline, as in Assist. Each hard requirement then needs its own
escape hatch in the stage machine, which is the defect being replaced.

## Forecloses

Any API that assumes one active activity per session, and any code that reads
`time.Now()` directly — barge-in correctness is a millisecond question, so the
supervisor owns an injected clock (CONTRIBUTING §1).
