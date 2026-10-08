# 0025. Keep voiceprints in a local file behind a minimal embed contract

- **Status:** accepted
- **Source:** SPEC §5, §10, §13, §4.3, §9.3, §14 item 4

Speaker fingerprinting is pulled forward because the barge-in gate (§4.3) and
stage three of wake confirmation (§9.3) both take a speaker id that nothing
produced. The id now comes from `internal/identity`: one centroid per enrolled
person, the L2-normalised mean of their guided-phrase embeddings (§5), cosine
matched per utterance. The model sits behind `identity.Embedder`, which
`internal/provider/speakerid` fills over HTTP from `sidecars/speakerid`,
SpeechBrain's ECAPA-TDNN (§10). Four decisions in that chain need a record.

**Centroids live in `identities.yaml`, next to `devices.yaml`, not in the
journal.** A voiceprint is a credential: it opens person-scoped tools and the
person's memory. SPEC §13 keeps secrets out of a database that would need a
separate backup, and a voiceprint is worse than a PSK on that axis because it
cannot be rotated. The file is gitignored, written atomically, owner-readable
only, and records the model and width it was enrolled with so a swapped
embedder is refused at load rather than scoring as noise. There is no example
file: the file is machine-written by enrollment and a hand-edited centroid is a
bug the loader reports by person.

**The embed contract is the repo's own.** There is no standard
speaker-embedding HTTP API the way there is for speech, so it is one endpoint
in the simplest shape that carries what the matcher needs: `POST /v1/embed`,
body `audio/pcm` in the satellite's own format (16 kHz, s16le, mono, no
header), reply `{"embedding": [...], "dim": 192, "model": "..."}`. Raw PCM
over multipart WAV because the host already holds exactly those bytes and a
header is one more thing to parse and get wrong (ADR-0023 found the same at
the TTS seam). `dim` and `model` are declared on every reply so the client can
refuse a sidecar that changed; the sidecar L2-normalises so a cosine is a dot
product. The dimension is the embedder's to declare: `identity` never assumes
192, and the file carries whatever was enrolled.

**The thresholds are placeholders.** `identity.DefaultAccept` is 0.25, the
default SpeechBrain's own `verify_batch` ships with, tuned on VoxCeleb trials;
`identity.DefaultMargin` is 0.05, a guess. Neither has been measured against
this embedder on household voices or on 16 kHz satellite audio, and the
constants say so. The measurement is the §4.3 corpus: every rejected candidate
is journalled with its score, and the thresholds move when that corpus says
where the household's voices actually separate. Until then a wrong threshold
fails safe, to guest.

**Ambiguous is unknown to the session.** When the best match clears the accept
threshold but leads the runner-up by less than the margin, the outcome carries
`identity.Ambiguous` and an empty id. The session's contract is binary (§5:
unknown or low-confidence means guest), and guessing between two enrolled
people would hand one person's context and tools to the other, then flip on
the next utterance. The reason is kept distinct rather than collapsed into
`BelowThreshold` because the two are tuned differently: ambiguity says
re-enroll or widen the margin, a low score says lower the threshold or fix the
audio path.

## Alternatives rejected

Centroids in Postgres beside the journal. It is where every other durable
thing lives, but it is also the thing that gets backed up, replicated, and
opened to a review UI, and a voiceprint has no business in any of those.

An explicit `ambiguous` branch in the session, giving the model both
candidates. Rejected for the same reason §4.3 rejects a semantic check: it
spends a decision where the user feels latency, and the cheap outcome (guest,
no interrogation) is what §5 already specifies.

## Forecloses

Nothing in the journal yet records the embedding. SPEC §5 says it is stored on
every trace record regardless of match, for clustering later; `Resolver.Resolve`
hands the vector back for exactly that, but the event schema is unchanged here
because a schema change regenerates the journal types, validator, and
reference docs, and that blast radius belongs in its own change. Until that
follow-up lands, the implicit-clustering corpus is not being collected.

The Hub revision of the checkpoint is `main` until someone with Hub access
records the commit. A rebuild could in principle pick up a newer checkpoint;
the client's model check would not catch it because the name is unchanged,
only a re-enrollment would. Pin it before any household is enrolled for real.
