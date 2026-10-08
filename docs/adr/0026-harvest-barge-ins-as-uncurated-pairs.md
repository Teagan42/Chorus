# 0026. Harvest a barge-in as an uncurated preference candidate

- **Status:** accepted
- **Source:** SPEC §9.1, §4.4, §8 · ADR-0005, ADR-0019

SPEC §9.1 says an interruption generates a preference pair: *rejected* is
what the assistant was saying, *chosen* is what it said after the correction.
The journal already holds every piece of that (ADR-0005, ADR-0007), so
`internal/harvest` is a reader over the log, like replay, and records nothing.
What it produces is a **candidate**, not a pair: the chosen side is left for
the Curate step, because what was said after the correction answers the
correction, not the prompt the rejected turn answered.

## The pair shape

A candidate carries the four texts the review kit edits, plus provenance:

- **Rejected** — everything the user heard of the cut turn, in order: each
  `speech_spoken.text` and the `spoken_text` of the `speech_truncated`.
- **RejectedUnheard** — what was generated and never played: the truncation's
  `unspoken_text`, then every `speech_discarded{barge_in}` of the same turn.
  It continues Rejected verbatim, so the two concatenated are the turn as
  generated, which is what the rejected message of a DPO row holds.
- **Heard** — the next `utterance_transcribed` after the cut, with its speaker.
- **AsSaid** — everything the user heard of the turn that answered Heard.

The prompt is the utterance the rejected turn answered, after the heard half
of up to four earlier turns (`ContextTurns`), oldest first. Only heard text
reaches the assistant side of a prompt: the model never saw the unheard
remainder, so a prompt that showed it would train on a context the model did
not have (SPEC §4.4). The system prompt is not inlined; `meta.versions.prompt`
names it, and a training run prepends the versioned artifact (SPEC §13).

Tool calls in the rejected turn are carried as context (`Calls`), never as
pair text. Sequence numbers of the anchoring events, the barge-in offset, and
the blob references for every clip ride along, because a reviewer cannot
judge a voice assistant from transcripts (ADR-0019).

```go
import "github.com/teaganglenn/chorus/internal/harvest"

// A raw candidate has no chosen side; the Curate step sets both.
func raw(p harvest.Pair) bool { return p.Chosen == "" && !p.Curated }
```

## Why chosen is not AsSaid by default

A barge-in happens because the answer was wrong for the prompt. The turn
after the correction is a good answer to *"just the first one"*, which is not
the prompt *"play something by zeppelin"* that the rejected turn answered. A
DPO row whose two sides answer different prompts teaches the model to answer
the correction before it has been given. The review kit's guard rejects
exactly that, so the harvester does not produce it: `Chosen` starts empty and
`Curated` false, and `Export` writes a `chosen` key only for a curated pair. A
raw row has no `chosen` at all and says `meta.curated: false`, so a loader
that requires the key fails on it instead of training on the wrong side.
`AsSaid` is still in the row's meta, since it is usually the right place for
a reviewer to start.

## What makes a candidate unattributable

Versions come from the `model_completed` of the rejected turn, the event that
produced the text (SPEC §8). A turn with no completion — the engine died
before `TurnEnd` — yields a candidate with `Attributed: false` and empty
versions. It is kept, not dropped: the cut and the correction are still worth
a reviewer's look, and the flag is what keeps it out of a training set. The
`speech_truncated` event carries versions too, and they are the same under
one journal, but they say what was in effect when the cut was recorded, not
what generated the text.

## What was deliberately not harvested

- A `speech_truncated` with no `barge_in_detected` in its turn. The session
  records a truncation for a preempt and for a migration as well, and neither
  is a correction by the person. Speech events carry no reason, so the
  detection is the discriminator.
- A cut that no correction followed: the session closed, the log ends, or a
  second barge-in landed before any utterance. These are counted
  (`Result.Uncorrected`), not paired, so a log full of them is visible as the
  gate-tuning problem it probably is (SPEC §4.3).
- A cut separated from its correction by a migration *is* harvested. The
  `session_closed{migrated}` / `session_opened{resumed}` sequence ends a
  session, not the conversation (ADR-0022), and the person's next utterance on
  the other device is the correction.
- The two other implicit signals in §9.1. "A completed turn with no
  correction is a weak positive" has no mechanical definition: a verbal
  correction without a barge-in looks exactly like a new request in the log,
  so every uninterrupted turn would qualify and the signal would be noise. "A
  repeated request is a failure" needs a similarity judgment the harvester
  has no business making. Both belong with the annotation vocabulary (§9.2).

## Forecloses

Speech events carry no `call_id`, so when a barge-in empties the queue the
harvester orders the truncated utterance ahead of the discarded ones by
reasoning (the playing one is at the head) rather than by record. That holds
for one cut per turn; a harvester that needs the queue order among several
discards must take it from the `tool_called` order and a call id the speech
events do not yet record.
