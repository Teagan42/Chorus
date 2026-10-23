# 0094. Read Smart Turn in bands, and journal what ended each turn

- **Status:** accepted
- **Source:** SPEC §4.5, §8, §11 · ADR-0036, ADR-0042, ADR-0048

Refines ADR-0036. Smart Turn answers a probability, and `listen.Semantic`
cut it at upstream's 0.5: anything over ended the turn at the pause, about
300 ms after the last word, and anything under held it for 2 s. A 0.55 and
a 0.99 were the same verdict. ming-voice, which layers the same model on a
voice-activity detector, reads it in three bands instead: sure ends at once,
doubtful waits long, and in between waits the ordinary pause. Chorus now
does the same, and the journal says which band each turn ended in.

```go
import (
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/stt"
)

// One endpointer per link, read in the household's bands. The words are read
// for any verdict that could end the turn, so Dangling takes the same doubt.
func endpointer(smartTurn listen.Judge, words stt.Transcriber, sure, doubt float64) *listen.Semantic {
	s := listen.NewSemantic(listen.Dangling{Judge: smartTurn, Words: words, Doubt: doubt})
	s.Sure, s.Doubt = sure, doubt
	return s
}
```
**The rule.** `listen.Judge` returns a `listen.Judgement`: Smart Turn's
probability, and whether the words dangle (ADR-0042). At the pause:

| Verdict | The turn | `turn_end` |
|---|---|---|
| words dangle | held to `DefaultHold` (2 s) | `dangling` |
| probability under `Doubt` (0.3) | held to `DefaultHold` | `held` |
| probability at or over `Sure` (0.8) | ends on the next chunk | `finished` |
| between | ends after `DefaultSilence` (800 ms), as Energy would | `unsure` |
| no verdict in time, or the judge failed | ends after `DefaultSilence` | `silence` |

"Unsure" ends where no judge would have: the model is not trusted to end a
turn early on a guess, and not trusted to hold one long on a guess either.
`listen.Dangling` now reads the words for every verdict at or over `Doubt`,
not only over 0.5, because an unsure verdict can end a turn too.

Speech resuming still cancels the pause, and ADR-0048's clock still times a
pending verdict. Equal bands are the old single cut.

**The journal.** `utterance_transcribed` carries `turn_end`, the rule above,
and `turn_probability`, the probability on the pause the turn ended on
(absent under `silence`). ADR-0036 named this gap: the journal could not say
which rule ended a turn, so nothing could tune the hold, the pause, or now
the bands. The listener reads it from the endpointer through `listen.Ender`.
Energy says nothing, and the fields are left out rather than claiming a
rule nobody applied.

**The bands are the household's.** `CHORUS_SMARTTURN_SURE` and
`CHORUS_SMARTTURN_DOUBT` override the defaults, refused at startup outside
0 to 1 or with doubt above sure. ADR-0084 moved the speaker threshold for the
same reason: a front end that rides its own gain shifts every score, and the
numbers below come from rendered voices, not a kitchen.

**Measured on 2026-10-23**, on this dev box's CPU, through the models tier
(`-smartturn-wavs`, `-stt-url`) against the sidecar served from its venv.
Hugging Face is unreachable from here, as for ADR-0042, so `corpus.py`'s 22
household takes were rendered with Kokoro v1.0 int8 in 16 voices (`af_heart`,
`af_bella`, `af_nicole`, `af_sarah`, `af_sky`, `am_michael`, `am_adam`,
`am_eric`, `am_liam`, `am_onyx`, `bf_emma`, `bf_isabella`, `bf_lily`,
`bm_george`, `bm_daniel`, `bm_lewis`): 352 takes, 192 finished and 160 cut
off. The words came from Parakeet TDT 0.6B v2 int8 under sherpa-onnx, behind
a stand-in for speaches.

Each take judged whole, with its words, and sorted by the rule it would end
on:

| | Single cut at 0.5 | Bands, 0.8 and 0.3 |
|---|---|---|
| Cut-off takes ended at the pause | 16 of 160 | 12 of 160 |
| Cut-off takes ended at 800 ms (unsure) | 0 | 5 |
| Cut-off takes held to 2 s (held or dangling) | 144 | 143 |
| Finished takes ended at the pause | 182 of 192 | 175 of 192 |
| Finished takes ended at 800 ms (unsure) | 0 | 10 |
| Finished takes held to 2 s | 10 | 7 |

So a quarter fewer half-said commands are cut at the pause, and three
finished ones the model doubted (0.40-0.45: "Good night.", "How long is left
on the dishwasher?", "Lock the front door.") wait 800 ms instead of 2 s. One
cut-off take the single cut held, "Turn off the," at 0.384 in a voice
Parakeet heard as "Turn off there", now ends at 800 ms.

Streamed in real time through `listen.Semantic` with its words, finished
takes ended 570-821 ms after the last loud chunk (median 757); 139 ended at
the pause, 15 as unsure, 4 were held. 34 finished and 18 cut-off takes ended
on `silence`: Smart Turn and a Parakeet decode, side by side on this CPU,
took longer than the 600 ms left of Energy's silence. That is ADR-0042's
cost on a busier box, not the bands, and `turn_end` is what will now show
how often it happens in a kitchen.

**What it costs.** Finished commands Smart Turn was unsure of now wait
Energy's 800 ms instead of about 300 ms: no worse than before ADR-0036. On
the corpus that is seven takes the model scored between 0.6 and 0.8, among
them "Turn on the porch light.", "Good night." and "Play something by Led
Zeppelin.".

**One finished take was called cut off.** Parakeet heard `af_nicole`'s
"Remind me to call my mom at six." as "... at six and", so the words held it,
and the models tier's ADR-0042 check failed on it. That check and the word
list are unchanged here; the take is one rendering, in one voice.

**What it does not fix.** Every cut-off take the bands still end at the
pause is one the transcriber misheard: "Turn off the," as "Turn off thy",
"they", "there", "those"; "Set the thermostat to," as "Set the thermostat
2."; "Set a timer for," as "Set a time or four." Smart Turn was sure, and
the words did not dangle as written. ADR-0042's word list cannot see a
mishearing, and the bands cannot either.

## Alternatives rejected

- **ming-voice's 500 ms middle wait.** Chorus's Energy waits 800 ms, and
  "unsure" means "as if nobody judged it". A shorter middle wait would be a
  second, unmeasured silence.
- **A short-utterance extra wait** (ming-voice adds 250 ms under 1 s of
  speech). Every cut-off take in the corpus is short because it was cut, so
  the corpus would flatter it, and "Good night." and "Lock the front door."
  would pay for it on every turn. The journal now records what a household
  needs to decide it from its own voices.
- **Asking Smart Turn at 64 ms of quiet, as ming-voice does.** ming-voice
  times the pause from a drop in energy because Silero VAD keeps saying
  "speech" for 120 to 190 ms after the voice stops; its 64 ms plus that
  hangover is about Chorus's 200 ms, which is measured from the energy drop
  already. Asking sooner here would add asks, and STT decodes, on every
  breath between words.
- **Silero VAD in front of the energy gate.** The gate reads the XMOS front
  end's processed channel, after echo cancellation and gain, and each
  satellite sets its own level (ADR-0083); Silero would be a per-frame model
  on the host for every link. Not this change: `turn_end` now says how often
  a turn ended on `silence` or `held`, which is what would show whether the
  gate misses pauses.
- **Recording only the rule, not the probability.** A band cannot be moved
  from a rule alone: the household needs to see how close its turns came.

## Forecloses

A cut-off command that Smart Turn is unsure of now ends 800 ms after the last
word rather than about 300 ms, so a person who pauses longer than that still
gets cut off; the bands buy time, not a guarantee. The journal grows two
short fields per utterance.

The models tier was run here; the Ollama tier, which no part of this
touches, was not.
