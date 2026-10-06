# 0008. Return recoverable failures to the model as tool results

- **Status:** accepted
- **Source:** SPEC §7

Everything recoverable becomes a tool result the model reasons about. A tool
timeout returns `{error: "timed_out"}` and the model says something true and
useful, instead of the orchestrator interrupting with "sorry, something went
wrong." Canned speech is reserved for LLM-unavailable and TTS-unavailable, where
no model is left to reason with.

Failures are first-class event types, because a failed turn is training data too.

## Consequences

Sessions do not survive an orchestrator restart in phase 1. The log replays for
debugging; resuming live audio across a restart is a lot of machinery for a case
solved by not restarting mid-sentence.
