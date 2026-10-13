# 0087. An answer in the room counts, and only another person's does not

- **Status:** accepted · supersedes the guest refusal in [ADR-0063](0063-the-yes-is-the-askers-and-a-guests-yes-runs-nothing.md)
- **Source:** SPEC §5, §6 · ADR-0038, ADR-0041, ADR-0085, ADR-0086

ADR-0063 refused a held call whose answer matched nobody enrolled, so that a
guest's yes ran nothing. On a Voice PE that made confirmation unusable: a
one-word yes carries too little voice to attribute (ADR-0085), so an answer
the household gave to a question the assistant had just asked *them* came back
`below_threshold` and was refused. Observed 2026-10-13, one call was held
thirteen times in three minutes while the household answered into a loop.
`journal.Redeemable` now refuses an answer only when **both** the asker and
the answer were identified and they are different people.

**Two states, not one.** "This is Bob, not Teagan" and "there was too little
voice to tell" are opposite, and only the first is evidence about who spoke.
ADR-0063 treated them alike because `journal.Unmatched` covers both
`below_threshold` and `ambiguous`; the check now keys on a positive
identification of somebody else instead.

**What the household accepts.** Anyone already in the room can answer a
question asked aloud in it -- a lock included. That is the decision, taken
knowingly: the household was shown that a dinner guest saying yes to "unlock
the front door" would unlock it, and chose it over a confirmation that cannot
be answered. The reasoning is that the question is spoken into a room someone
is already standing in, next to the door it is about, so the yes is not the
control that keeps them out. An answer identified as another *enrolled*
person is still refused, which is the case where the log knows enough to
object.

**A transient is not an answer.** This decision depends on ADR-0086: an
utterance holding under `listen.DefaultMinSustained` of speech-level energy is
treated as silence. Before it, eleven mouse clicks entered one conversation as
"Yeah." and "No.", and under this ADR any of them would have redeemed the
lock. The sustain floor, not the speaker check, is what keeps a click from
answering a question.

## Alternatives rejected

**Tiering by what the call acts on** -- a session-scoped yes for lights and
media, identification or a longer phrase for locks, covers and doors.
`registry.Target` already carries the domain and classes `ConfirmWhen` reads
(ADR-0041), so it is cheap to build, and it was offered. The household chose
the flat rule. Kept here because it is the obvious next move if the flat rule
ever proves too loose, and because nothing in the code forecloses it.

**Requiring a longer confirming phrase** ("yes, open it") so the answer is
long enough to attribute. It fixes the measurement and not the experience:
the assistant would be rejecting ordinary answers to its own question and
teaching the household a password.

## Forecloses

`journal.RefusedGuest` is no longer produced. It stays declared so a log
written before this still reduces and reads, and `confirmationNote` still has
its case, but no new refusal uses it. A reader counting guest refusals to
tune the gate has no new data after this.
