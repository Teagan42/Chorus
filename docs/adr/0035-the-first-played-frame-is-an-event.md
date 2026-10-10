# 0035. The first played frame of a turn is an event

- **Status:** accepted
- **Source:** SPEC §11, §8, §3.2.1, §9.2 · ADR-0030, ADR-0033

SPEC §11 sets a 700 ms first-audio target and asks for honest measurements,
but the journal recorded speech only when it ended: `speech_spoken` and
`speech_truncated` land after the DAC drains or stops, so no reader could say
how long anyone waited. The Speaking child now journals `speech_started` the
moment the device's PLAYED report first moves past the position the turn's
first utterance began at (§3.2.1), once per turn. The event's wall clock is
when the household first heard the answer, and `wait_ms` is the wait
since the person stopped speaking, both read from the injected clock
(CONTRIBUTING §1). It is the number Triage's slow-turn signal and the
"too slow" label (§9.2) are judged against.

```go
import "github.com/teagan42/chorus/internal/journal"

// The wait a reader judges a turn by is a field of the event, not a
// difference between two events' wall clocks.
func waited(e journal.Event) string { return e.Fields["wait_ms"] }
```

The wait starts when speech stopped, not when the endpointer fired: the
household waits through the endpointer's trailing silence (800 ms by default,
`listen.DefaultSilence`) too, so it counts. The listener backdates the End by
the quiet the endpointer reports waiting through (`listen.Trailer`); the mic
streams in real time, so bytes of quiet are that long ago. With the energy
endpointer alone that silence exceeds the 700 ms target, and Triage says so on
every turn until semantic endpointing (§10) shortens it. When the listener has
no clock, the ask carries no stop time and the event omits the field rather
than inventing a start.

Only a stream that can see its DAC reports a start. `session.Starter` is an
optional interface, so a speaker that cannot see playback records nothing
rather than a guess. The satellite's report lags the DAC by at most one PLAYED
interval, and that is the error bar on the figure.

## Alternatives rejected

- **The endpoint as the start.** Lower numbers, but it hides the one
  interval every turn pays and that the endpointer alone controls.
- **The wake word as the start**, as §11 words the target. That counts how
  long the person talked, and a follow-up turn has no wake word at all.
- **The wall-clock gap between `utterance_transcribed` and `speech_spoken`.**
  The transcript is journalled after decoding and speaker matching, and the
  speech event after playback ends, so that gap counts neither the wait
  before nor the time spent listening.
- **A start time on `speech_spoken`.** Speech that is cut before it ends is
  still speech that started, and an answer whose first utterance was
  discarded still has a first frame. One event per turn keeps the figure in
  one place.

## Forecloses

The figure is per turn, not per utterance: the gap between a filler ("one
sec") and the answer behind it is not recorded. If that matters later, it is a
second event, not a change to this one.
