# 0083. A satellite sets its own speech gate, because its front end rides the gain

- **Status:** accepted · refines [ADR-0036](0036-smart-turn-judges-the-pause-energy-stays-the-floor.md)
- **Source:** SPEC §4.5, §13 · ADR-0030, ADR-0073

`listen.DefaultSpeechEnergy` is -40 dBFS, and its own comment said the figure
was "unmeasured on a real room". Measured on a Voice PE 2026-10-13 it is below
that room's noise floor: the XMOS AGC held an empty room at -35 dBFS median
and -38.5 dBFS at the tenth percentile, so **98% of silence counted as
speech**. The longest quiet run in a 30 s recording was 40 ms, against the
200 ms `listen.DefaultPause` that asks the judge and the 800 ms
`listen.DefaultSilence` that ends a turn. So Smart Turn was never asked at
all, every turn ran to its cap, and each clip reached speaker ID and the
embedder as a few seconds of speech inside half a minute of amplified room.
`speech_energy` in `devices.yaml` now overrides the default per satellite;
zero keeps it.

**Per satellite, not a new default.** The gate belongs to a front end, not to
Chorus: the Satellite1's differs and has never been measured this way, and one
room's number must not be imposed on a board nobody measured. The same reading
argues the default is wrong for a Voice PE and right to leave alone until each
board has its own measurement.

**Why a level and not a better endpointer.** The chain is already the right
shape -- energy finds the pause, the judge rules on the words (ADR-0036). It
was one constant below the floor that stopped the judge from ever being
consulted, so the fix is the constant. Measured against the same recordings,
0.05 (-26 dBFS) keeps 13.1% of windows, which is the true speech fraction of a
clip holding 4 s of speech in 30 s, and yields quiet runs of 1.5 s to 6 s on
every clip tried, short ones included. 0.02 was still inside the noise and
0.08 began cutting speech.

## Alternatives rejected

**Gating on the pre-AGC channel instead.** Channel 1 carries the same audio
before the AGC (ADR-0073), so its floor is honest and a fixed gate would suit
it. It was measured ~47 dB quieter, which is a second calibration rather than
none, and the bridge sends channel 0 as the utterance every other stage reads.
Changing which channel endpoints is a larger decision than a level.

**Trimming silence out of what is embedded.** Tried, and it does not reach the
cause: the turn still runs to its cap, so a reply still waits for it. Cutting
non-contiguous voiced windows together also scored *worse* than leaving the
gaps in -- 0.893 to 0.651 against a household's own voiceprint -- so the
embedder wants continuity, not density. Trimming stays where it is useful, on
an enrollment take (ADR-0082).

## Forecloses

Nothing in the barge-in gate, which still reads `listen.DefaultSpeechEnergy`
through `session.Gate.MinEnergy` and is shared by every link, so it cannot
take a per-satellite value without being built per link. A room whose floor
defeats the endpointer defeats that gate the same way; this does not fix it.
