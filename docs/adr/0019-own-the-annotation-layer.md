# 0019. Own the annotation layer; emit OpenTelemetry alongside it

- **Status:** accepted
- **Source:** SPEC §9.1, §9.2, §8

The primary learning target is the LLM's tool-calling and speech-interleaving
decisions; STT adaptation on household voices is a free byproduct. The
highest-value artifact is automatic: an interruption yields a DPO preference
pair — *rejected* is what it was saying, *chosen* is what it said after the
correction — and it works only because the truncation point is exact
([ADR-0005](0005-interrupted-turn-truth.md)). A completed turn with no
correction is a weak positive; a repeated request is a failure.

The review UI is Go plus htmx over Postgres, in this repo, with a fixed
annotation vocabulary for the ambiguous cases — *transcript wrong*,
*misunderstood intent*, *wrong tool / wrong args*, *should have spoken and
didn't*, *spoke when it shouldn't*, *too slow*, *wrong person attributed*,
*good — exemplar* — plus free text and edit-and-replay.

## Alternatives rejected

Delegating annotation to Langfuse or a hosted trace tool. OpenTelemetry is
emitted alongside the journal so those tools give exploration for free, but the
labels are the training set and they stay with the data (§8).

## Consequences

Inline per-turn audio playback is non-negotiable: a voice assistant cannot be
judged from transcripts.
