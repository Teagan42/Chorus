# 0090. An impulse is told from a voice by its shape, not its length

- **Status:** accepted · supersedes the sustain floor in [ADR-0086](0086-a-transient-is-not-speech-and-a-held-call-is-given-up-on.md)
- **Source:** SPEC §4.5, §6 · ADR-0083

ADR-0086 disbelieved a decode from an utterance holding under 200 ms of
speech-level energy, on the strength of three mouse clicks holding 20, 40 and
120 ms against speech holding 740 to 2520 ms. The comparison was wrong: that
speech was **sentences**. A one-word answer is as brief as a click. Measured
2026-10-13 after the floor shipped, a household's "Yes" to a held
confirmation was disbelieved twice at 40 and 60 ms of sustained energy, so
the confirmation could not be answered at all -- the loop ADR-0086 set out to
fix, reintroduced by its own fix.

The axis that separates them was in ADR-0086's own measurements and went
unused. **Crest factor**, peak over RMS: the three clicks sat at 24.3, 24.9
and 25.5 dB; five real utterances from the same room, loud and quiet, one word
and several, sat at 13.7, 14.5, 16.2, 17.3 and 20.4 dB. A click is a spike in
a quiet room and a voice is not, whatever either one says.

**Both conditions, not either.** `listen.Impulsive` disbelieves a decode only
when the audio is *both* brief and spiky: under `DefaultMinSustained` of
speech level *and* over `DefaultMaxCrest` of 22 dB. Either alone catches real
speech -- brevity catches a quiet word, and a high crest factor alone would
catch a sharp consonant in an otherwise silent clip. Run over every clip
measured, the rule rejects all three clicks and believes all six utterances,
including the quiet 40 ms one the floor killed.

## Alternatives rejected

**Lowering the floor instead.** The quiet word and the quietest click both
hold 40 ms, so no floor separates them: the distributions do not merely
overlap, they coincide.

**Reading the crest factor alone.** 22 dB is four dB clear of the loudest
voice measured, which is a thin margin to hang a turn on when a clip that is
mostly silence around one plosive can reach it. Requiring brevity as well
costs nothing -- a real click is both -- and keeps the margin for the case it
was measured on.

## Forecloses

Both numbers come from one room and one front end, and they are global rather
than per satellite like the speech level is (ADR-0083). A room where a voice
reaches 22 dB of crest factor would lose words to this, and the only sign
would be the log line naming what was not believed.
