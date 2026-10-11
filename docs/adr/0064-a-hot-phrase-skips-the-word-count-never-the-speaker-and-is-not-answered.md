# 0064. A hot phrase skips the word count, never the speaker, and is not answered

- **Status:** accepted · refines [ADR-0004](0004-barge-in-detection-gate.md)
- **Source:** SPEC §4.3, §4.4, §9.1, §10 · ADR-0004, ADR-0026, ADR-0031, ADR-0033, ADR-0045, ADR-0049

Refines ADR-0004, which it does not supersede: the stacked gate stands,
and this records one exception to its third stage. The gate refused a
partial shorter than `MinWords`, two in chorusd (`minBargeInWords`), as
`partial_length`. So "stop", "cancel", "quiet" and "enough", the most
common things a household says to a voice assistant, could not interrupt
it, and a timer going off in an empty kitchen could not be silenced at all:
the listener left the mic alone while an announcement nobody may answer
played (ADR-0045). "Never mind" and "say that again" reached the model only
after endpointing, which is far too slow for a stop.

## Decision

**A small closed set of hot phrases**, in one place in Go
(`internal/session/hotword.go`):

- *stop*: stop, cancel, quiet, enough, shut up, be quiet, stop it, that's
  enough;
- *never mind*: never mind, nevermind, forget it, forget that;
- *repeat*: say that again, repeat that, what did you say, come again.

A phrase must be the whole of what was said, after lowercasing and
dropping punctuation, so "stop the music in the kitchen" is a request. It
may follow "okay", "ok" or the assistant's name said without the wake word
("Eddie, stop"), and may be said over and over ("stop, stop").

```go
import "github.com/teagan42/chorus/internal/session"

// Teagan over the forecast, and a request that only begins like a stop.
func heardAsStop() bool {
	return session.Hot("Eddie, stop.") == session.HotStop && session.Hot("Stop the music.") == ""
}
```

The set is closed and English-only on purpose. Every entry is a way to
stop the house on one word, past a filter, so it grows only by a decision;
and the ear is Parakeet, which is English-only (SPEC §10).

**It skips the word count and nothing else.** Energy still gates, and so
does the speaker stage: the television saying "stop" is refused at
`speaker_id`, which is the case SPEC §4.3 calls the high-value filter. With
no speaker identifier configured, the stage is skipped for a hot phrase as
for anything else (ADR-0031). The phrase is recorded as `hot_word` on
`barge_in_detected`, and on `barge_in_rejected` so the tuning corpus can
see a refused "stop" apart from a refused "uh".

**What each one does.**

- *Stop* is an ordinary barge-in that one word may make: speech stops at
  the report that answers the stop (ADR-0033), and the turn's pending work
  ends under each call's `on_interrupt`, as any barge-in's does (SPEC §4.4).
- *Never mind* does the same, and is also offered to the gate while a turn
  is working with nothing playing, the slow search the person no longer
  wants. Nothing else is a candidate then.
- Once the person stops talking, an utterance that is wholly a stop or a
  never mind, and that stopped something through the gate, is journalled
  with `hot_word` and not asked about. The cut was the answer; asking the
  model would talk over it. The next turn is still told it was said.
- *Repeat* says again what the person last heard: the heard words of the
  latest turn that said anything, from the log (`journal.State.LastSaid`),
  so a cut answer is repeated only as far as it was heard. It is a speak
  call the session makes, marked `"repeats":true`, played through the
  speech channel like any speech, without the model. With nothing said yet
  it is an ordinary turn.

A hot phrase that stopped nothing, such as "stop" as the first thing said
after a wake, is an ordinary turn, as it was before.

**Over an announcement nobody may answer**, the listener now segments
speech and offers the gate a partial that is a stop or a never mind, and
nothing else. What is said over it is never a turn, and only the prefix
the gate judged is kept, as a candidate's audio always is (ADR-0060).

**A cut a hot phrase answered is not harvested.** SPEC §9.1 pairs the cut
turn with what was said after the correction. A stop or a never mind has
no after: the person asked for silence, and there is no chosen side for a
curator to find in it. A repeat says the answer was unheard, not wrong.
Either way the harvester counts the cut in `Result.Hushed` and drafts no
pair; the cut, the phrase and the audio stay in the log. The repeat's
speak call is chosen by no turn, so like a canned line (ADR-0051) it is no
side of any pair. A barge-in on a hot partial whose utterance went on to
ask for something, "stop the music in the kitchen", is paired as any
correction is: readers go by the utterance's `hot_word`, not the
detection's.

## Alternatives rejected

- **Lowering `MinWords` to one.** "Uh" and a cough would stop the house;
  the stage exists for them.
- **A substring match.** It would make "stop the music in the kitchen" a
  stop and leave the music playing.
- **A stop that silences speech but leaves the turn's work running.** SPEC
  §4 makes a barge-in cancel Speaking and apply each call's interrupt
  policy. A stop that did less would leave a model loop acting, unheard,
  after the person asked it to stop, and `detach` and `uninterruptible`
  already keep the work that must outlive a cut.
- **Emitting a stop as a pair with an empty or marked chosen side.**
  ADR-0026 already leaves `chosen` empty on a raw row, but a curator can
  fill one in; for a stop there is nothing to fill it with. A row that can
  never be completed is noise in the review queue, and a marker every
  export must remember to filter is the failure ADR-0026 was written to
  prevent. A count keeps the signal visible without putting it in the
  corpus.
- **A new event kind.** The phrase is a property of a detection, a
  rejection and an utterance that already exist.
- **A configurable phrase list.** Each entry bypasses a filter, and a list
  anyone can extend is a gate anyone can widen.

## Forecloses

- **English only.** A multilingual ear (SPEC §10) needs its own set.
- **The name is in the code.** "Eddie" is a leading word, as it is a filler
  in `internal/triage`; renaming the wake word means editing both.
- **"Stop" with nothing playing reaches the model.** Said while a turn
  works in silence it is no candidate and is asked about once the turn
  ends; "never mind" is the phrase that reaches the work.
- **A repeat repeats only what was heard.** The unheard remainder of a cut
  answer was never said, and is not said now.
- **A stop before the DAC played any of a timer** leaves it unheard, and
  the scheduler says it again within its grace (ADR-0045).
- **What is said over an announcement has no event of its own** beyond the
  candidates the gate judged.
