# 0046. A turn that calls nothing has its answer spoken from its content

- **Status:** accepted
- **Source:** SPEC §4.1, §5 · ADR-0003, ADR-0043, ADR-0044

Speech is a tool call. Content is dropped, because with `think: false`
qwen3:4b reasons aloud there, and phi4-mini once echoed the whole tool
schema into it. The household's models-tier runs found qwen3:14b skipping
the tool exactly when it already knew the answer:

- Asked for a remembered garage code, it reasoned that it "didn't need any
  tools", and wrote `speak` on one line and the code on the next.
- Asked "what did I ask you yesterday", it wrote last night's garage door.
- Asked "what day is it", it wrote "Today is Friday, 9 October 2026."

Each turn ended with nothing heard. A prompt clause saying an answer
already known is still a speak call did not stop it. Now the decoder
speaks that content when the turn ends.

```go
import "github.com/teagan42/chorus/internal/session"

// What the session receives from the turn that wrote "speak\nThe garage
// door code is 4512." and called nothing: one implicit utterance, then the
// end of the turn.
var spoken = session.SpeechDelta{Text: "The garage door code is 4512.", Mode: session.ModeQueue, Last: true}
```

**Content is an answer only in one shape.** All four must hold:
- the turn called no tool;
- it ended with `stop`, not cut off by its length;
- the model's reasoning came in the endpoint's own thinking field;
- the content is not empty once any `</think>` is dropped.

Then the content is spoken once the turn ends, as one queued utterance with
no call id. The session records it as an implicit speak, as it does inline
content (ADR-0003). The speak tool's name leading the content is the call
the model meant to make, and is not said aloud: `speak` on a line of its
own, `speak:` before the words, or `speak "…"` on one line, whose quotes are
dropped too. Content that only starts with the word, like "Speaker volume
in the kitchen is at forty percent.", is said whole.

**Reasoning in content is still never spoken.** With `think: false` there
is no thinking field, so the content is the reasoning and stays unheard,
as `content_leak.ndjson` shows. Content beside a tool call is not spoken
either: the turn said what it meant through its calls.

**The prompt still asks for the tool.** Its rationale no longer claims
content is never heard, since it now can be.

## Alternatives rejected

- **Turn on `SpeakInlineContent` for qwen3:14b.** That streams content as
  it arrives, beside tool calls too, so "Let me check" content plus an
  `ha_get_state` call would be said twice over. It also ties correctness
  to a per-model flag.
- **Keep rewording the prompt.** One clause was tried and measured, and the
  model still answered in content in the next run.
- **Recover any line naming any tool.** Only `speak` carries words to say.
  A line naming another tool is a call the model failed to make, and saying
  its arguments aloud would be worse than silence.

## Forecloses

- **The answer is heard after the turn ends, not as it streams.** For a
  turn that is nothing but the answer, that is the same moment.
- **A model with thinking off that answers in content is still unheard.**
  Its content cannot be told apart from reasoning.
- **Measured twice.** The three answers above are from one models-tier run
  on the household's Ollama. The fixtures `answered_in_content.ndjson` and
  `day_in_content.ndjson` are rebuilt from that run's output, in the
  shape Ollama streams. The rerun with this decoder, ten times over each
  of those questions and the remembered-memory ones, failed once. Asked "how do I take my coffee", qwen3:14b read it as how to
  brew it and wrote `speak "Would you like instructions on how to brew your
  coffee, or are you looking for something else?"`, which was said with
  the tool's name and quotes. That shape is now stripped
  (`question_in_content.ndjson`). The misreading is the model's, and is
  left to it.
