# 0048. Time Smart Turn's verdict by the clock, not by the audio

- **Status:** accepted
- **Source:** SPEC §3.3.2, §4.5 · ADR-0036, ADR-0042

Refines ADR-0036. Its rule says a turn with no verdict by 800 ms ends where
Energy would. `listen.Semantic` counted those 800 ms in audio bytes, but the
judge answers on the clock.

Audio that arrives faster than it was spoken therefore reached Energy's
silence before the judge had had time to answer. SPEC §3.3.2 measured that
kind of audio on the living-room Satellite1: uplink gaps of over a second on
a congested evening, then the buffered frames in one burst. After a burst
like that, "set a timer for… twelve minutes" ended at "for" and reached the
model as two turns, the case ADR-0042 holds.

**The rule.** When the judge is asked, it is given the rest of Energy's
silence on the clock (`Semantic.Clock`): 800 ms less the quiet already
heard. While that time lasts and no verdict has landed, the turn is held,
but never past the 2 s hold (`listen.DefaultHold`).

A verdict that lands still applies on the next chunk, as before. A judge
that runs out of time ends the turn on the next chunk, which in real time
is where Energy would have ended it.

The clock is the listener's, the daemon's own (ADR-0035), so a test that
fixes it sends audio as a burst does. An endpointer with no clock is timed
by the audio alone, exactly as ADR-0036 was.

Each verdict is logged at debug level ("semantic endpointing judged the
pause"). The daemon's tests wait on that line instead of on the ask, so the
quiet they send next lands after the verdict.

Measured on 2026-10-10 with six parallel copies of the `cmd/chorusd` tests
under `-race`, 40 runs each:

| Test | Before | After |
|---|---|---|
| `TestSmartTurnShortensTheWaitForAnAnswer` | 17 of 240 failed | 0 of 240 |
| `TestACutOffCommandReachesTheModelWhole` | 11 of 240 failed | 0 of 240 |

`TestABurstAfterARadioStallWaitsForSmartTurn` plays the stall through the
whole daemon with the verdict held back, and it fails without this rule.

## Alternatives rejected

- **Fix only the tests.** They were failing because of this behaviour, and
  the burst they simulated by accident is one the satellite produces for
  real.
- **Wait for the verdict with no ceiling.** A hung sidecar would hold every
  turn. The hold already bounds a wrong "unfinished", so it bounds a missing
  verdict too.
- **Pace the uplink on the host before it reaches the endpointer.** That
  delays every turn by the stall, including the ones whose verdict was
  already in.

## Forecloses

A burst with a slow judge can now hold a turn for up to 2 s of audio, where
it used to end at 800 ms. In real time nothing changes: the clock and the
audio agree.
