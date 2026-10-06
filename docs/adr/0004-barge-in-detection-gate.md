# 0004. Gate barge-in on speaker identity, not semantics

- **Status:** accepted
- **Source:** SPEC §4.3

Barge-in detection stacks three cheap filters — VAD energy, speaker-ID match
against the session speaker or a known household member, then an STT partial
length gate — targeting ~300 ms from user speech to TTS stop. Speaker ID is the
high-value filter: the television and the wrong housemate both fail it, which
energy thresholds cannot do.

## Alternatives rejected

A semantic "was this addressed to me" check. It spends latency exactly where
latency is felt, and the error costs are asymmetric: a wrong stop is cheap
because the model keeps talking, while a slow stop feels broken.

## Consequences

Every rejected candidate barge-in is journalled (`barge_in_rejected`) as the
tuning corpus for this gate, so threshold changes are evaluated against
recorded reality rather than guessed.
