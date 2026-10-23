# 0097. A speaker ID that fails is recorded as failing, and readiness asks each sidecar what it serves

- **Status:** accepted · refines [ADR-0031](0031-skip-the-speaker-stage-when-nothing-identifies-speakers.md), [ADR-0068](0068-chorusd-serves-health-readiness-and-metrics-derived-from-the-journal-on-loopback.md), [ADR-0069](0069-say-a-failure-the-household-would-meet-as-silence-and-show-what-cannot-be-said.md)
- **Source:** SPEC §4.3, §5, §7, §9.3, §13 · audits/sidecars-2026-10-10

A speaker embed that failed came back from `Listener.resolve` as an empty
`identity.Outcome`, which every caller read as a voice it had judged and
found to be nobody. A wake was rejected as `unknown_speaker`, which is a
guest's wake held for review (ADR-0058). A barge-in was refused at
`speaker_id`, which is the television in the gate's tuning corpus. A wrong
`SPEAKERID_URL` or a sidecar rebuilt on another model therefore looked like a
house full of strangers. Startup said speaker ID was on, and the log held one
warning line per utterance.

**A failure is its own outcome.** `identity.Failed` (`speaker_match:
failed`) is no judgement, like `too_short`: the listener records it whenever
the resolver errors.

- **A spoken wake still fails closed**, since stage three cannot confirm a
  household voice (SPEC §9.3). It is rejected as `speaker_id_failed`, with its
  audio. Nothing judged the voice, so it is not held as a guest's and does not
  ship as a hard negative (`harvest.HardNegative`). The household gets no
  apology: an embedder that cannot say who spoke cannot tell Alan from the
  television either, and an apology to the television is a false accept they
  would hear.
- **The ring shows the fault.** `session.FaultSpeakerID` is raised on the
  failure and cleared on the next embed that answers (ADR-0069). This is the
  one sign a household has that its wakes are going nowhere.
- **Inside a session the speaker stays who they were.** `journal.Attribute`
  already left the speaker alone for anything but `below_threshold` or
  `ambiguous`. Recording `failed` rather than an empty match keeps that
  behaviour and lets a reader of the log tell an outage from a log written
  before matches were recorded.
- **A barge-in is still refused, as `speaker_id_failed`.** Only a recognised
  voice stops speech while speaker ID is configured. While the embedder is
  failing, nobody can interrupt. That is Teagan's call (2026-10-23), made over
  skipping the stage as ADR-0031 does for a house with no identifier: an
  outage should not hand the television the floor. The new stage keeps those
  refusals out of what tunes `speaker_id`.

**Readiness asks what is served, not only whether something answers.**
ADR-0068's probes asked `/health` and took any 2xx. A speaker-ID sidecar on
ECAPA-TDNN, or a Smart Turn sidecar on another checkpoint, answers `/health`
and has every call refused by a client built for the pinned model (ADR-0025,
ADR-0036). Both sidecars state their model on `/health` (speaker ID its width
too), so `speakerid.Client.Health` and `smartturn.Client.Health` compare them.
Kokoro's probe asks `/v1/audio/voices` instead of `/health` and requires the
voice the synth speaks in, each part of a blend included. An unknown voice is
a 400 on every utterance, which nothing reported until an answer went silent.
Each mismatch shows as the dependency down in `/readyz`, with both names in
its error, and as the "dependency down" warning from the first probe round.
The household sees it before anyone has said a word.

## Alternatives rejected

**Failing the wake open while speaker ID is down.** SPEC §5 and §9.3 already
decide this: with no working resolver, chorusd cannot tell the television
from a person, so a spoken wake does not open a session.

**Skipping the speaker stage at barge-in, as ADR-0031 does.** It was the
recommended option, because it keeps "stop" working through an outage. It was
declined because the speaker stage is the television filter, and an outage is
exactly when nobody is watching for the television.

**Refusing to start when a sidecar is down or mismatched.** Speaker ID and
Smart Turn are optional and each degrades to something that works (no
attribution, Energy's silence). A daemon that will not start over either
would leave the house with no voice at all, which is worse than either
degradation. Readiness and the ring are where a running daemon says what is
wrong.

## Forecloses

A barge-in during a speaker-ID outage cannot succeed. A household that wants
interruptions to keep working through an outage has to unset `SPEAKERID_URL`,
which is ADR-0031's gate, and accept the television.
