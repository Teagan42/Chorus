# 0082. A take may be a clip the satellite itself kept, trimmed to what was said

- **Status:** accepted · refines [ADR-0080](0080-the-review-ui-enrolls-the-household-into-the-same-file-and-a-browser-take-is-made-the-satellites-format-before-it-is-sent.md)
- **Source:** SPEC §5, §8, §9.2 · ADR-0025, ADR-0029, ADR-0070, ADR-0076

ADR-0080 said a laptop's microphone is not a satellite's and that a
voiceprint from it matches less surely. Measured on a Voice PE 2026-10-13,
that cost is the whole feature: a household enrolled from the browser scored
0.406 against 30 s of its own cleanly-transcribed speech, under the 0.70
`identity.DefaultAccept`, so every spoken wake was refused as
`unknown_speaker`. The Voices screen now also offers the clips chorusd
already kept from a chosen satellite, which are that satellite's own front
end's output, as takes.

**The reviewer's ear is the attestation.** A clip is audio from a room, not a
guided phrase, so nothing in the log says whose voice it is -- which is why
`docs/voice-enrollment.md` told a household not to enroll from the journal.
The screen answers that the way the review UI answers it everywhere else: the
clip plays in the row it is ticked in, and a reviewer who has heard it is the
one asserting it is a person and not the television. That is the same standing
a reviewer already has to drop a clip from a satellite's own log (ADR-0076).

**Only the listed satellite's own clips.** A ticked ref is refused unless it
is one the screen offered for that satellite, so no other blob the store holds
can be made into a voiceprint -- the assistant's own speech under `tts/` above
all. The satellite comes from the dropdown, and a name that is not a log the
journal has is replaced by one that is, so a form cannot name a log nobody
has.

**A clip is trimmed; an upload is not.** A satellite's recording is mostly
room: the measured 30 s clip held one 4 s sentence, and the embedder pools
over all of it, so the silence outvoted the voice. `identity.TrimSilence`
cuts to the voiced span, padded, by window energy relative to the loudest
window. An uploaded take stays untouched, as ADR-0080 has it: it is the
household's own file, held to the format and otherwise left alone. A clip whose
trimmed span is under `identity.MinVoicedSamples` is **left out of the list**,
not offered and then refused: the list is twelve rows and unpaged, so a row
nobody can tick costs one that someone could. The floor is 3.5 s, set above
the longest take that failed rather than the shortest that worked -- two 1 s
slices of one clean voice scored 0.499 against each other, and a 2.8 s clip
of an enrolled voice scored 0.268 against that person's own voiceprint, while
the takes that built a usable one ran 3.2 to 4.4 s. `readClip` keeps the same
check, so the list and the check cannot drift apart in silence.

## Alternatives rejected

**Resampling or gain-matching a browser take to the satellite.** It treats a
spectral difference as a level one. The XMOS's AGC and noise suppression
change the envelope the embedder reads; the measurement above is after the
device's own AGC had already brought the speech to −1 dBFS.

**Capturing from the device on demand instead.** Worth having, and not
exclusive with this: it needs a control path between the review UI and
chorusd, which owns the links, and a review UI reachable off the host would
then command a microphone in the house. That is its own decision, with its
own authorisation.

## Forecloses

Nothing about per-satellite voiceprints: `identity.Person` still carries one
`Centroid`, and takes from two satellites average into it. A household whose
satellites differ enough to need a print each needs that schema change, which
this does not make.
