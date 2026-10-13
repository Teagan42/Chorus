# 0085. Too little voice to judge leaves the speaker speaking

- **Status:** accepted · refines [ADR-0049](0049-a-voice-that-matched-nobody-is-a-guest-and-the-voice-the-gate-refused-is-not-a-turn.md)
- **Source:** SPEC §5, §9.3 · ADR-0031, ADR-0084

`journal.Attribute` makes an utterance a guest's when `speaker_match` is
`below_threshold` or `ambiguous`, and that is right: each judged the voice and
found it was nobody enrolled. But a score from a second of audio is not a
judgement. Measured on a Voice PE 2026-10-13, a household's own ordinary
commands scored 0.45 to 0.63 at 1.4 to 2.8 s of speech and 0.72 to 0.89 at
four seconds and over, so a conversation fell out of attribution the moment
its owner said something short -- which is most of what anyone says to a
house. `identity.TooShort` is a fifth `speaker_match`: no verdict, so the
current speaker keeps the turn, as they already do when the field is empty.

**The score stops being evidence before the ranking does.** Those same short
commands ranked their own speaker first by 0.18 to 0.63 over the next person.
So the conversion happens only when the nearest centroid is the session's
speaker *and* it led the runner-up by `Thresholds.Margin`. A guest chiming in
does not lead the person whose session it is, and neither does a television.
Both of those are guarantees with tests behind them (ADR-0049): the first
attempt at this ADR converted every short below-threshold utterance and
promptly handed a session to a guest and to the television, which is what
`TestAGuestChimingInIsHeardAsAGuestNotAsAlan` and
`TestATelevisionThatTalkedOverSpeechNeverBecomesATurn` exist to prevent.

**It needs a second person enrolled to work at all.** `Outcome.Leads` is
false when nobody else is enrolled to be beaten, because one centroid is
nearest to every voice in the house and a lead over nothing is not a lead.
A single-person household therefore keeps the old behaviour exactly, which is
the safe direction to fail in.

**The first utterance is exempt.** A wake is confirmed against an enrolled
voice or not at all (SPEC §9.3), and there is no session to inherit from
anyway. This only ever relaxes attribution *inside* a session that an
identified person already opened.

## Alternatives rejected

**Taking the user's wording literally** -- any utterance too short to identify
is the same speaker. It is the obvious reading and it breaks the two
guarantees above. Recorded because the request will be made again.

**Lowering the accept threshold further instead.** ADR-0084 already moved it
to fit this audio; moving it far enough to admit a 1.5 s command would put it
under the 0.43 a second enrolled voice reached, which admits strangers. The
threshold answers "is this them"; this answers "was there enough to ask".

## Forecloses

Attribution now depends on who the session already belongs to, so a replay
has to reduce in order to reproduce it. It already did -- `journal.Attribute`
is the reducer's own function and the only path either side uses -- but a
future reader that attributes an utterance in isolation will get a different
answer than the session did.
