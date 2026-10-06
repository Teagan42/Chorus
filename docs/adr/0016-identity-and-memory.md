# 0016. Attribute every utterance, and gate memory sharing by default

- **Status:** accepted
- **Source:** SPEC §5

Identity is explicit enrollment through guided phrases plus a per-utterance
speaker embedding, cosine-matched against enrolled centroids. The session holds
a current speaker, but a confident mismatch flips attribution **within the same
conversation** — a second person chiming in is a real case Assist cannot
handle. An unknown or low-confidence speaker gets guest context with
person-scoped tools gated, and no interrogation.

The embedding is stored on every trace record regardless of match, which gives
implicit clustering for free later.

Memory is explicit `remember` / `forget` tools plus an auto rolling summary per
person, both injected by relevance. Person context is global across satellites;
the satellite contributes **location as turn metadata, not identity**.

## Consequences

Cross-person visibility is off by default, with an explicit per-memory
`shareable` flag. This is a household rather than a tenancy, but surprising
disclosure is how a voice assistant loses trust.
