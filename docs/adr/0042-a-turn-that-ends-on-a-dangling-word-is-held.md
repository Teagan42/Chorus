# 0042. Hold a turn whose words end on one no command ends on

- **Status:** accepted
- **Source:** SPEC §4.5, §11 · ADR-0024, ADR-0036

ADR-0036 measured Smart Turn v3.2 calling "Set a timer for," finished in all
four Chatterbox voices, and "What's the weather on," and "Remind me to call
my mom and," with it: a short command that trails off on a preposition or a
conjunction, on a falling pitch, sounds done. The turn ended about 300 ms
after the last word, and the model was asked for a timer with no duration.
The audio cannot tell these apart. The words can, so every pause Smart Turn
calls finished is now checked against them.

```go
import (
	"github.com/teaganglenn/chorus/internal/listen"
	"github.com/teaganglenn/chorus/internal/stt"
)

// Smart Turn decides; the words can only hold the turn open.
func endpointer(smartTurn listen.Judge, words stt.Transcriber) listen.Endpointer {
	return listen.NewSemantic(listen.Dangling{Judge: smartTurn, Words: words})
}
```

**The rule.** `listen.Dangling` is a `listen.Judge` around Smart Turn. At
each pause it sends the same audio to Smart Turn and to the transcriber the
turn is decoded with, side by side. Only a "finished" is checked:
`listen.Dangles` says whether the transcript's last word leaves the command
unsaid, and if it does the verdict becomes "unfinished", so the turn is held
to `listen.DefaultHold` (2 s) or until speech resumes, as ADR-0036 holds any
unfinished turn. "Unfinished" and errors from Smart Turn pass through
unchanged, and a failed decode leaves Smart Turn's verdict as it was.
Neither can end a turn sooner than before. A pause whose decode is still
running at 800 ms ends there, as an unanswered judge always has.

**The words that dangle.** A closed list, small on purpose: a wrong "cut
off" costs the person the 2 s hold.

- Articles and possessives: *the, a, an, my, your, our, their, its, every,
  each, another.*
- Prepositions that never finish a command: *for, to, of, with, at, from,
  into, onto, by, as, until, between, without…*
- Conjunctions: *and, or, but, because, if, unless, than*; *then* after
  *and* or *or*.
- Fillers: *uh, um, er, hmm.*
- Particles (*on, off, up, down, in, out, over, back*) dangle unless their
  clause has something for them to finish: a verb that takes one and has
  not already ("turn the lights on", "wake me up"), a question about state
  opened by a copula ("is the oven on"), or a copula just before ("what's
  on"). So "what's the weather on", "turn on the lights in" and "can you
  play music in" dangle, and "leave the porch light on" does not.

*That, this, her, about* and *like* are left out: "what's that", "call
her" and "what's the movie about" are whole.

**Measured on 2026-10-09**, on this dev box's CPU, through the models tier
(`-stt-url`). Hugging Face is unreachable from here (ADR-0036), so Chatterbox
could not render ADR-0036's corpus. The same 22 takes from `corpus.py` were
rendered instead with Kokoro v1.0 int8 in four voices (`af_heart`,
`am_michael`, `bf_emma`, `bm_george`), 88 takes. The words came from Parakeet
TDT 0.6B v2 int8 under sherpa-onnx, behind a stand-in for speaches, as in
ADR-0024's measurements. Both model files come from GitHub releases.

| What | Smart Turn alone | With the words |
|---|---|---|
| Finished takes judged finished | 47 of 48 | 47 of 48 |
| Cut-off takes judged unfinished | 25 of 40 | 39 of 40 |
| Finished turns end after the last loud chunk | 266–384 ms, median 335 | 416–725 ms, median 544 |
| Cut-off turns held to the 2 s hold | 26 of 40 | 39 of 40 |
| One decode of a take | | 161–423 ms, median 266 |

On Kokoro, Smart Turn missed more than on Chatterbox: 15 cut-off takes, all
ending on *for, to, on, the* or *and*, "Set a timer for" in every voice
again. The words held 14 of them. The one left is "Set the thermostat to,"
which Parakeet wrote as "Set the thermostat 2.": nothing in the words dangles.
No finished take was called cut off. The one finished miss is Smart Turn's,
"How long is left on the dishwasher?" in one voice, and the words cannot
change it.

**What it costs.** A finished turn now waits for the slower of Smart Turn
and a decode. On this CPU that is the decode, about 200 ms more than Smart
Turn alone, and still under Energy's 800 ms. A decode on a GPU would cost
less. The decode at the pause covers nearly the same audio as the utterance's
final decode (ADR-0024), which runs after the End. Reusing it would take
that cost back. That is a change to `stt.Utterance`, not to this rule.

## Alternatives rejected

- **Fine-tuning Smart Turn on household cut-offs.** Upstream trains on
  Hugging Face data this box cannot reach. It would need a recorded corpus
  the repo may not hold (CONTRIBUTING §7). And the words already close 14 of
  the 15 misses.
- **The last STT partial instead of a decode.** Partials lag the mic by
  0.5–1.6 s (ADR-0024's measurement in `internal/stt`), so at a 200 ms pause
  the newest one often lacks the last word.
- **Checking the words only after Smart Turn says finished.** Decoding
  second rather than alongside puts both latencies on every finished turn.
- **Overruling "unfinished" when the words look whole.** A complete-sounding
  transcript is common mid-sentence ("turn off the kitchen lights… and the
  hall"), and an early End is worse than a held one.
- **Asking the turn-engine LLM.** SPEC §4.5 rules it out by name.

## Forecloses

Every pause now costs a decode as well as a verdict, so the STT sidecar sees
about one more request per turn. The word list is English, like Parakeet
TDT 0.6B v2 (SPEC §10). A multilingual transcriber would need a list per
language or this check switched off. A cut-off the transcriber mishears
("to" as "2") still ends the turn, as before.
