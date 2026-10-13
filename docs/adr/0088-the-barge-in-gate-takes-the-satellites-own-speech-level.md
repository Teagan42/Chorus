# 0088. The barge-in gate takes the satellite's own speech level

- **Status:** accepted · refines [ADR-0083](0083-a-satellite-sets-its-own-speech-gate-because-its-front-end-rides-the-gain.md)
- **Source:** SPEC §3.1, §4.3 · ADR-0004, ADR-0049, ADR-0086

ADR-0083 gave each satellite its own speech level and said, under
Forecloses, that `session.Gate.MinEnergy` still read the built-in default
and was shared by every link, so a room whose floor defeated the endpointer
defeated that gate the same way. It did. Measured 2026-10-13 on a Voice PE:
**140 barge-in candidates in one evening, every one rejected at
`speaker_id` and none at `vad`.** The energy stage did no work at all, so
each candidate cost a transcription and an embedding before the speaker
stage discarded it.

The supervisor is built per link already, so the gate is now built there with
the satellite's own level. Zero keeps the default, as everywhere else.

**Nothing was falsely interrupting.** The speaker stage was doing its job:
the assistant's own voice is not an enrolled household voice, so leakage past
the AEC never stopped speech. What this fixes is cost and a log that buries
real rejections under noise -- the corpus that tunes the gate (SPEC §4.3) is
worth less when most of it is a room.

Two mechanisms were measured, and only one is an energy problem. A candidate
at -34.4 dBFS RMS with a 13.3 dB crest factor held 1180 ms over the default
and 20 ms over the room's own level: that is the room, and the gate now stops
it. A candidate at -30.7 dBFS with a 15.9 dB crest factor held 640 ms over
the room's level and decoded to no words at all: that is the device's own
speech coming back, it is shaped like speech because it is speech, and no
energy threshold separates it. The speaker stage is what answers it.

## Alternatives rejected

**Allowing barge-in only while the device is not playing.** Barge-in is by
definition the interruption of speech in progress; outside playback there is
nothing to barge into. Full duplex is why `chorus_bridge` exists rather than
`voice_assistant` (SPEC §3.1), and this would return the satellite to
half-duplex with extra steps. Recorded because the suggestion follows
naturally from seeing played audio in the candidate log.

**A crest-factor test to catch the device's own voice.** Measured, and it
does not separate: leakage sits at 15.9 dB against a household's 14 to 20 dB,
because both are speech. Impulses do separate on crest factor, which is what
the sustain floor uses them for (ADR-0086), but an echo of a voice is not an
impulse.

## Forecloses

Nothing about using the played audio itself. The host knows what it sent and
when the device played it (SPEC §3.2.1), so a reference-signal check could
answer leakage directly rather than leaving it to the speaker stage. That
needs the downlink kept beside the uplink for comparison, which nothing does
today.
