# 0095. A turn is asked in its pause, and kept only if the confirmed turn would have asked the same

- **Status:** accepted · builds what [ADR-0007](0007-journal-is-the-runtime.md) and [ADR-0014](0014-turn-engine-cascade-first.md) left for later
- **Source:** SPEC §4.5, §8, §11, §15.5

Every turn waits out its pause twice. The endpointer holds the end of an
utterance through 200 ms to 2 s of quiet before it ends it (ADR-0036), and only
then does the ear decode it -- a quarter to half a second for a short command
on the CPU (`stt.DefaultPartialEvery`'s measurement) -- and only then is the
model asked. With `CHORUS_SPECULATE=on`, the work starts at the pause instead:
the quiet that sends the audio to Smart Turn (`listen.DefaultPause`) also
decodes it, identifies the speaker, and asks the model, which streams into a
buffer. Nothing is dispatched and nothing is played. ming-voice does the same
with its Thinker and holds every state-changing tool back in the same way.

**The rule that keeps replay honest is one comparison.** The answer is kept
when the turn is confirmed only if the `session.Input` the confirmed turn
derives from the log -- after its `utterance_transcribed` is appended -- is the
one the speculative ask was made with (`sameAsk`). The speculative input is
folded from the same log by the same reducer, over the utterance the pause
supposed (`journal.Suppose`), so in the ordinary case they agree by
construction, and in every other case -- the person kept talking, a timer went
off between them, a detached tool landed, the speaker was judged differently,
the minute the model is told turned over -- the answer is thrown away and the
model asked afresh. A kept answer is then indistinguishable in the log from one
asked late: the same events, in the order the model chose them, its calls
dispatched as their JSON replays out of the buffer. `model_completed` adds
`asked_early_ms`, how long before the turn was confirmed its ask began, so the
Slow tab can see what speculation bought.

**What is thrown away is journalled once, as speculative.** A discarded ask is
one `speculation_discarded` event, declared `speculative: true` in
`schema/event.cue` and refused by the journal unless appended as speculative,
carrying the words it guessed, why it was discarded and the completion as far
as the model got. The reducer already skips speculative events (SPEC §11), so
replay of a conversation is the same with or without them, and every reader
that walks the log sees committed work only unless it asks. That is the
distinction SPEC §15.5 kept from day one; this is the first writer of it.

The early decode is reused when the pause it was taken at is the one that
ended the utterance -- no loud chunk since -- so the confirmed turn carries
the words, speaker and attribution the ask was made with, and the final decode
is not run at all. A pause the person talks through cancels the decode and
discards the ask.

**Off by default.** It spends a decode, an embedding and an ask on every pause
a person speaks through, on hardware the household shares with the ear; the
Slow tab should show it is worth that on a given machine before it is on.

## Alternatives rejected

**Journalling the speculative ask's events as they happen and promoting them
on commit.** Every reader -- the reducer, harvest, the review UI, the eval
suite, the export -- would have to learn to hold speculative `tool_called` and
`model_completed` until a commit event arrives, and to put them after the
utterance they preceded. Keeping the log in the shape a late ask writes means
none of them changes.

**Comparing the transcript only.** The words agreeing does not make the
answer the confirmed turn's: a memory recalled, a result landing or a timer's
announcement between the pause and the end changes what the model is told. The
whole input is what the completion is a function of, so the whole input is
what is compared.

**Not recording the discards.** A discarded ask cost a GPU second and saw a
guess at what the person said; leaving it out would make that cost invisible
and contradict SPEC §11's claim that the distinction exists.

## Forecloses

Only turns inside a live session speculate. The wake's own utterance -- the
one that confirms the wake and opens the session -- is asked late, because no
session exists to fold its log from until the wake is confirmed. Speculating it
means supposing the session's open as well, from a conversation index that
must not move; that is the next step, and it is where most of SPEC §11's
700 ms is.

The voice is not rendered ahead: a kept answer's speech starts synthesising at
the commit. Rendering into the speech channel before it is confirmed would
need a speaker that can hold audio unplayed and drop it, which
`internal/satellite` does not have.

The Dangling judge and the early decode decode the same pause twice.
