# 0084. The accept threshold is configurable, because a front end's gain compresses every score

- **Status:** accepted · refines [ADR-0029](0029-speaker-id-on-onnx-runtime-pinned-by-digest.md)
- **Source:** SPEC §5, §9.3, §13 · ADR-0025, ADR-0031, ADR-0073, ADR-0083

`identity.DefaultAccept` is 0.70, measured on enrollment-quality takes where
no cross-speaker take passed 0.676 and the lowest own take was 0.659. On a
Voice PE's processed far-field audio the whole distribution shifts down:
measured 2026-10-13 with two voices enrolled from that device's own
recordings, a household member's ordinary commands -- "is the front door
locked", "open the garage door please" -- scored **0.45 to 0.79** against
their own voiceprint, while the second enrolled voice never passed **0.43**
against it. Every one of those commands was refused as `unknown_speaker`
although it ranked its own speaker first by a margin of 0.18 to 0.63.
`CHORUS_SPEAKER_ACCEPT` now overrides the threshold; unset keeps 0.70.

**The ranking survives what the score loses.** The margin between the right
person and the next is what stayed intact, so the fix is the level the
threshold sits at and not the model, the enrollment or the audio. Lowering it
to 0.50 on the measured device accepts every clip of the household's own
speech and leaves 0.075 between that and the best score the other voice ever
reached. `Thresholds.Margin` is unchanged and still refuses a verdict whose
runner-up is close, so a second guard remains.

**Global, not per satellite.** Unlike `speech_energy` (ADR-0083) the resolver
is built once and shared by every link, and the threshold is a property of the
model and the enrolled centroids as much as of one front end. A household
whose satellites differ enough to need a threshold each needs a resolver per
link, which this does not build.

**The default does not move.** 0.70 stays what ADR-0029 measured. A household
lowers it against recordings from its own room, with more than one voice
enrolled so a cross-speaker figure exists at all; `.env.example` says so,
because this is the one number between the household and the television
(ADR-0031).

## Alternatives rejected

**Identifying on more audio.** The scores rise with length -- 4 s and over
reached 0.72 to 0.89 -- so a longer utterance would pass unchanged. But real
requests are 1.5 to 2.5 s of speech, and `bridge.TypeWake` carries the word
without its pre-roll (ADR-0030), so the wake phrase is not available to pad
them. Worth doing, and it does not remove the need for a threshold that fits
the audio the device actually sends.

**Trusting the margin alone.** Attractive, since the margin is what survived,
but it fails closed in the wrong direction: with one person enrolled there is
no runner-up, so every voice in the house would rank first by the whole of its
score. An absolute floor is what keeps the television out.

## Forecloses

A per-satellite threshold, until the resolver is built per link. A household
mixing a close-mic satellite with a far-field one has to pick one level for
both, and the lower of the two is the one that admits strangers on the other.
