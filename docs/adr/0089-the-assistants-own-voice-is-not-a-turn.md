# 0089. The assistant's own voice is not a turn

- **Status:** accepted · refines [ADR-0049](0049-a-voice-that-matched-nobody-is-a-guest-and-the-voice-the-gate-refused-is-not-a-turn.md)
- **Source:** SPEC §3.1, §4.3 · ADR-0064, ADR-0085, ADR-0088

Observed 2026-10-13: the assistant asked "did you want to open the garage
door", its own answer came back through the microphone four seconds later,
decoded as "May I open the rush?", and it answered itself as a guest. A voice
that matched nobody is a guest and a guest is heard (ADR-0049), which is right
for a person and wrong for the device. An utterance that no household voice
owns and that arrived while the device was playing is now dropped.

**Nothing about the audio separates them.** On the channel the bridge sends,
the echo measured -19.2 dBFS RMS against a household's -18.8 dBFS, at the same
length and the same crest factor: the AGC brings both to the same place. The
pre-AGC channel is ~50 dB down for *both*, so it does not discriminate either
-- it is quieter, not cleaner. Level, duration and crest were all measured and
all fail. Only the speaker tells them apart.

**Sampled while the utterance is open, not when it is decoded.** The first
cut read the session's children in the decode path and let the echo through
whenever playback finished first, which a test caught. `overSpeech` is set as
the utterance opens and again on every candidate, so a long echo is still
known to have talked over an answer.

**What still barges in.** An identified voice, as before -- full duplex is why
`chorus_bridge` exists (SPEC §3.1). And a voice the ranking put with the
session's own speaker, which `identity.TooShort` marks (ADR-0085): that is
only produced when the nearest centroid was the session's speaker and led the
runner-up, which a synthesised voice does not reach. So a household member
whose "stop" is too short to score still interrupts.

**What no longer does.** A guest, while the assistant is speaking. ADR-0049
keeps a guest audible and this narrows it to silence, knowingly: the same
window is the only one an echo can arrive in, and nothing in the audio
distinguishes the two. A guest speaking after the answer finishes is heard as
before. Hot phrases are not carved out -- ADR-0064 already has a hot phrase
skip the word count and never the speaker, and exempting them would let the
echo stop the speech it is an echo of.

## Alternatives rejected

**Allowing barge-in only while the device is not playing**, which was the
first suggestion. It removes barge-in rather than fixing it (ADR-0088).

**Comparing the transcript with what was just spoken.** The echo decoded as
"May I open the rush?" against "did you want to open the garage door" -- the
ear garbles its own output as readily as anything else, so a text comparison
would have to be loose enough to catch that and would then catch a person
agreeing with the answer.

## Forecloses

Still nothing about using the played audio as a reference signal (ADR-0088),
which is the answer that would let a guest keep speaking over an answer. The
host knows what it sent and when the DAC played it (SPEC §3.2.1); comparing
the uplink against it is the work nobody has done.

A dropped echo is logged and not journalled, so it is not in the corpus that
tunes the gate. A kind for it is the obvious follow-up and needs a schema
change.
