# 0096. The device's own voice is told from a person's by what it played

- **Status:** accepted · answers the Forecloses of [ADR-0088](0088-the-barge-in-gate-takes-the-satellites-own-speech-level.md) and [ADR-0089](0089-the-assistants-own-voice-is-not-a-turn.md)
- **Source:** SPEC §3.2.1, §4.3 · ADR-0031, ADR-0033, ADR-0083

ADR-0088 and ADR-0089 both ended on the same unbuilt answer: the host knows
what it sent the speaker and where the DAC was when it played it, so the
uplink can be compared against that rather than leaving the device's own
voice to the speaker stage. This builds it, after the echo guard in
[ming-voice](https://github.com/JMS1717/ming-voice) (MIT), which keeps the
played audio as a reference and judges each mic frame against it.

**The reference.** The Speaking side places every rendered delta on an
`echo.Reference` at the DAC frame it will play from, `SpeechBase` plus the
frames before it, before any of it is sent. A barge-in's cut drops
everything after the position the stop's answer reports (ADR-0033), because
the device discarded it and the room never heard it. One per link: the DAC's
count is.

**The comparison.** Each barge-in candidate's newest 800 ms is compared, by
the Listening child, against the reference from 100 ms ahead of the DAC's
newest report to 600 ms behind it -- the firmware publishes PLAYED before it
pumps the uplink, then capture buffering, send batching and the speaker's
underrun silence put the echo behind. The feature is 24 log-spaced bands from
150 Hz to 6 kHz, 32 ms frames every 10 ms, each row taken relative to its own
mean: the spectrum's shape, not its loudness. The score is the best mean
per-band correlation over the mic's voiced rows, at the satellite's own
speech level (ADR-0083). Under 300 ms of voice, or a reference silent at
every lag, is no verdict.

**The gate.** A new stage, `echo`, after `vad` and before `speaker_id`,
refuses a candidate scoring at or over the satellite's `echo_threshold`
(default 0.6). Before the speaker stage, so a refused echo costs no
embedding -- ADR-0088 counted 140 candidates in one evening, each a decode
and an embedding before the speaker stage threw it away. And whatever the
speaker stage says, so it holds with no speaker ID at all (ADR-0031), where
the speaker stage is skipped and the device's own voice, read as two words,
used to stop the answer it was an echo of. Every candidate compared, admitted
or refused, journals `echo_score` and `echo_lag_ms`: a real barge-in's score
is the other half of what tunes the threshold.

**Measured** on the household's own day (`internal/reviewui/household`):
every answer the assistant gave, in its Kokoro voice, through
`echotest.Kitchen` -- a speaker driven into its knee, a room's reflections, a
canceller off by 12%, the residual 10 dB under a person and brought up by an
AGC to -19 dBFS where ADR-0089 measured both on a Voice PE, 140 ms behind the
DAC -- against every person's clip a quarter second into every answer.
`TestTheHouseholdsDayAgainstItsOwnAnswers` holds these:

| At 0.6 | Called an echo |
|---|---|
| The device's own voice | 125 of 127 |
| A person, nothing leaking | 2 of 580 |
| A person over the residual | 2 of 580 |

Both people called an echo are Teagan saying "unlock the front door" and
"lock the front door" while the device said "Unlock the front door?": the
same words in time are the device's words. Off the table's room: with the
residual only 3 dB under the person, 59 of 580 (10%) are called an echo; at
20 dB under, 2 of 580. With the echo 20 ms behind the DAC, 19 of 580 (3%);
at 400 and 580 ms, 2 and 1. Every echo window was caught at all three delays.

**A wrong verdict costs a partial, not the barge-in.** Each partial is a new
candidate on a newer window, so a person the stage refused is judged again
half a second later with more of their voice in it. The truncation point is
untouched: the position is read before anything is compared, and the cut is
the device's answer to the stop. A judgement takes about 2.7 ms on a 4-vCPU
CI box, ahead of an embedding it usually spares.

## Alternatives rejected

**Loudness envelopes in 8 bands**, the first cut. Every voice rises and falls
with its syllables, so two unrelated ones line up on loudness by chance: a
person alone scored up to 0.61 at the 95th percentile against the echo's
0.61 at the 5th. Taking each row relative to its mean, the spectrum's shape,
dropped the person's 95th percentile to 0.39 with the echo's 5th at 0.66.
Judging only the newest 300 ms at the lag the whole window found made both
worse.

**Comparing the waveform**, which is what ming-voice does against a
browser's canceller. Not measured here: the XMOS chain adds noise
suppression and an AGC after the canceller, which keep when a band carries
the voice and not its phase, and a simulated room cannot say how much phase
survives a real one.

**A signal from the satellite.** Its VAD calls the echo speech, because it
is (ADR-0088), and the pre-AGC channel is 50 dB down for the echo and a
person alike (ADR-0089). The kit's direction of arrival (ADR-0061) would
place the speaker at a fixed bearing, but the Voice PE has two mics and no
DoA, and the reference works on both.

## Forecloses

ADR-0089's drop is left alone: an utterance over speech that no household
voice owns is still not a turn, even when the stage judged it a person.
That drop is what a guest's chance to speak over an answer waits on, and the
2 echo windows in 127 the stage misses would come back as the assistant
answering itself. The scores the journal now keeps, from a real room, are
what would justify narrowing it.

The threshold and the lag range come from a simulated room and synthetic
voices. A front end whose residual sits nearer a person's level calls more
people echoes; the only sign is people refused at `echo` with scores just
over the threshold, which is why both halves are journalled.

A candidate with under 300 ms of voice is not compared and falls to the
speaker stage. At the default partial cadence (`stt.DefaultPartialEvery`,
half a second of audio) the first candidate is past it; with no speaker ID,
anything shorter is still unjudged.

No lag is learned. ming-voice locks onto the echo's delay once matches agree
and judges near it with a lower threshold; this searches all 700 ms every
time, which is what lets a chance match reach 0.6.
