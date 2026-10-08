# 0033. Tag each stop; the report that answers it is the truncation point

- **Status:** accepted · refines [ADR-0005](0005-interrupted-turn-truth.md) and [ADR-0020](0020-correct-device-api-facts.md)
- **Source:** SPEC §4.4, §3.2.1, §15.1 · commits `2d82097`, `26a27a0`, `26aa7ee`

The truncation point is the position the firmware's counter held when the
STOP reached it: the counter is gated there and the frames the DAC drains
afterwards are deliberately not counted. The host learned that position from
the PLAYED report that followed the stop, and until now it had to guess which
report that was. `settle` sampled the position, sent the stop, and returned on
the first position change after it. But the firmware publishes PLAYED at
`loop()` cadence and a report takes several milliseconds to arrive, so a
report emitted *before* the stop can still be on the wire when the host
samples; it is the change `settle` returned on, and the report the stop itself
provoked landed after `Close` had already read the position. The recorded cut
was then the DAC's position about one round trip earlier -- always short of
what was heard, usually inside the 32-character segment the text split
resolves to, so the words were rarely wrong, but `frames_played` was short of
the DAC by tens of milliseconds. SPEC §15.1 says that number cannot be fixed
later.

The fix is on the wire. Every STOP carries a tag in its flags byte, folded
from the utterance generation `Link.Stop` already bumps and never zero. Once
the firmware has gated the counter and stopped the speaker, it emits exactly
one PLAYED with that tag in its flags, whether or not the position moved since
its last report, and retries it rather than dropping it when TX is full,
because no later routine report can stand in for it. The satellite waits for
the report echoing its tag and for nothing else; the settle deadline remains
only for a link that fails under the stop. Protocol version is 2.

```go
import "github.com/teaganglenn/chorus/internal/bridge"

// The cut is on the one report that answers the stop; a routine report
// carries no tag and may predate it.
func placesTheCut(p bridge.Played, tag uint8) bool { return p.Stop == tag }
```

## Alternatives rejected

**Host-side quiescence** -- after the first change, keep waiting until no
further report arrives for a quiet window of a couple of firmware report
intervals. It is cheaper, needing no firmware change, but it is still an
inference about timing: a network slow enough to delay the post-stop report
past the quiet window reproduces exactly the error being fixed, and the
window lengthens every cut's journal record by its own duration whether or not
anything was in flight. The device knows which report follows the stop; the
host can only guess, and SPEC §3.2.1 and ADR-0020 already put the position on
the device's side for the same reason.

**A new frame type** for the answer instead of a flag on PLAYED. It would
widen `bridge.Handler` for every consumer of the link, and a tag echoed in a
byte the header already carries costs the firmware one byte load and the host
nothing.

**Accepting both versions at the handshake.** A version 1 device ignores the
tag and never answers, so a version 2 host would wait out the whole settle on
every barge-in and then record the same stale cut as before, with no symptom
louder than slightly wrong training data -- the failure mode ADR-0020 warned
about. Both halves live in one checkout and version together (ADR-0017), so
the handshake refuses the mismatch and the error names the firmware as the
half to flash.

## Forecloses

Mixed-version deployments. A host built from this checkout will not take a
satellite flashed from before it, and a satellite flashed from it announces
version 2, which a host from before it refuses; every satellite is reflashed
with the host that serves it. The residual error is on the device, not the
wire: the counter is gated by a lock-free flag the speaker task reads, so one
DMA callback already past the check can land after the answer is taken. That
is bounded by a single DMA buffer, sub-millisecond, and independent of the
network, where the old bound was a round trip. The firmware change has been
validated with `esphome config` and not yet flashed; the hardware tier's
barge-in test waits for the echoed report and will say whether the real
device answers.
