# Architecture decision records

Hand-written, numbered, never edited after acceptance — superseded instead
(CONTRIBUTING §3). Every ADR cites the SPEC section or commit it was sourced
from; `docs/SPEC.md` stays normative and an ADR only records why a clause reads
the way it does. Start from [`0000-template.md`](0000-template.md).

These were backfilled from `docs/SPEC.md` and the commit log, grouped by
decision rather than by clause: facets of one choice share one record.

| ADR | Decision | Source |
|---|---|---|
| [0001](0001-scope-single-household.md) | Target one household, not a product | §1 |
| [0002](0002-actor-supervisor-session.md) | Model a session as a supervisor, not a pipeline | §4 |
| [0003](0003-speak-is-a-tool.md) | Concurrent action stream; `speak` is a tool | §4.1, §12 |
| [0004](0004-barge-in-detection-gate.md) | Gate barge-in on speaker identity, not semantics | §4.3 |
| [0005](0005-interrupted-turn-truth.md) | Interrupted-turn truth and an exact truncation point | §4.4, §3.2.1 |
| [0006](0006-conversation-keyed-on-person.md) | Conversation keyed on person, audio on device | §4.5 |
| [0007](0007-journal-is-the-runtime.md) | The event journal is the source of truth | §8, §11 |
| [0008](0008-failures-are-tool-results.md) | Recoverable failures return to the model as tool results | §7 |
| [0009](0009-dial-out-native-api-client.md) | Be a native API client that dials the device | §3.1, §3.3.1 |
| [0010](0010-chorus-bridge-external-component.md) | Audio over a raw socket from an external component | §3.1, §3.2 |
| [0011](0011-registry-owns-policy.md) | Native tool registry owns policy; MCP is a provider | §6 |
| [0012](0012-cue-is-the-source-of-truth.md) | Author invariants in CUE; commit generated output | CONTRIBUTING §2 |
| [0013](0013-local-first-model-stack.md) | Local-first models behind capability-declaring providers | §10 |
| [0014](0014-turn-engine-cascade-first.md) | Turn-engine interface, cascade before S2S | §12, §11 |
| [0015](0015-two-stage-wake-confirmation.md) | Confirm the wake word server-side | §9.3 |
| [0016](0016-identity-and-memory.md) | Per-utterance attribution; memory sharing off by default | §5 |
| [0017](0017-monorepo-polyglot-by-seam.md) | One repo, polyglot by seam | §2, §13 |
| [0018](0018-mechanical-enforcement.md) | Enforce the five rules with tools, not review | CONTRIBUTING |
| [0019](0019-own-the-annotation-layer.md) | Own the annotation layer; OTel alongside | §9.1, §9.2 |

SPEC §15, the decisions that cannot be retrofitted, maps to ADRs 0003, 0005,
0006, 0007, and 0011. Those five carry the whole phase-1 risk.
