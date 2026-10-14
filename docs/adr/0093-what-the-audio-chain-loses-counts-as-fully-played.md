# 0093. What the audio chain loses counts as fully played

- **Status:** accepted · refines [ADR-0033](0033-the-stop-is-tagged-and-its-answer-is-the-cut.md)
- **Source:** SPEC §3.2.1, §4.4 · ADR-0051, ADR-0073

`satellite.stream.Close` reported an utterance fully spoken only when the
device's own frame count reached every frame sent. Measured on a Voice PE
2026-10-13 it never does:

    device confirmed  43482 of  43490 frames
    device confirmed 125532 of 125539 frames
    device confirmed  91303 of  91311 frames

Seven or eight frames, half a millisecond, out of utterances three to eight
seconds long. The board's downlink runs 16 kHz through a resampler and a mixer
into a 48 kHz DAC (ADR-0073), and the chain rescales the i2s callback back to
its input rate; the last partial output block never flushes into that count.
So every utterance reported `playback_unconfirmed`, `speak` answered the model
with an error, and the model said everything again -- twice, sometimes three
times, on every single turn.

`playedSlack` is **8 frames, the measured figure with no margin added**. A
wider tolerance is not safer, it is worse: the host cannot tell a chain that
lost its last block from a device that stopped a hair early, so widening it
swallows real truncations. A first attempt used 2 ms -- four times the
measurement -- and broke three interruption tests, which is exactly that
failure appearing as a red suite rather than as a household being talked over.

**It cost three attempts and all three were the same mistake.** Letting the
drain finish early also collapses the window in which a barge-in arriving
during it is seen, and ADR-0033 makes that window the truncation point. Only
tolerating what was measured leaves it intact.

## Alternatives rejected

**Accepting the shortfall only once the position goes idle.** Correct in
principle and unnecessary once the tolerance is the measured size, and the
test doubles dispatch `Timers.After` by duration -- a short new timer fires
immediately in one fake and on an unrelated clock advance in another, so it
would have been load-bearing and invisible.

**Fixing it on the device.** The loss is inherent to the resampler's final
partial block, so reporting exactly would mean the firmware tracking what it
has not yet converted. The host tolerating a measured constant is smaller and
testable here.

## Forecloses

The constant is one board's. A chain with a larger block, or one converting by
a different ratio, loses a different number of frames and would need its own,
with nothing to say so except every utterance reporting unconfirmed again --
which is how this one was found, after it had been mistaken for a slow
synthesiser and for a drain that was too short.
