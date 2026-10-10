# 0037. Ask the model again with its tool results, and tell it the dialogue from the log

- **Status:** accepted
- **Source:** SPEC §4.1, §4.4, §7, §8 · ADR-0003, ADR-0005, ADR-0007, ADR-0008

Until now every ask sent the model the system prompt and one transcript, and
nothing else. A tool's result reached the journal and stopped there. When
Teagan asked whether the garage door was closed, the model read the cover and
was never asked again, so it could not say. A second utterance arrived with
no memory of the first, so "and the porch light too" meant nothing.
SPEC §4.4 also says the model sees what was cut. Now a turn asks again after
every set of tool results, and every ask carries the conversation so far,
derived from the log.

```go
import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// A follow-up ask ends on what the utterance's calls returned.
func endsOnAResult(in session.Input) bool {
	n := len(in.Dialogue)
	return n > 0 && in.Dialogue[n-1].Kind == journal.EntryResult
}

// One utterance gets at most this many asks.
const asks = session.DefaultRounds
```

**The dialogue is derived, not kept.** `journal.State.Dialogue` is folded by
the reducer like the rest of the state. A replay therefore tells the model
exactly what the live session told it (ADR-0007), and no second copy of the
conversation can drift from the log. It holds four kinds of step, in log
order:

- **What was heard:** each final transcript.
- **What was said:** the words the person heard, settled from playback. A cut
  answer carries only its heard half and is marked cut. Speech nobody heard
  is dropped (SPEC §4.4).
- **Each call** other than speak.
- **Each result**, whatever its outcome. A failure is a result the model
  reasons about (ADR-0008).

Speculative work never enters the dialogue.

**Speech sits where the model said it.** A speak step takes its place at the
speak call. Playback fills in the heard words later, which is why
`speech_spoken` and `speech_truncated` now carry the `call_id` they played.
In a log from before that field existed, the words land where playback
finished. An utterance that arrives whole records its words with its call.
Every Ollama speak arrives whole. So an ask made while "Let me check." is
still playing knows what it is saying, and the step is marked as still
playing.

**The session decides when to ask again; the engine only says it.** After an
ask's stream ends and its calls return, the session asks again when both of
these hold:

- The model called something other than `speak` or `end_session`.
- The turn is still live. A barge-in, a closing session or a lost journal
  write ends it.

`session.DefaultRounds` caps one utterance at four asks. Finding an entity,
acting on it and saying what happened takes three; the fourth is slack. Each
ask's completion is journalled as its own `model_completed`, because replay
cannot regenerate any of them (SPEC §8). Each ask's inline speech gets its
own call id.

**Ollama gets the dialogue as chat messages:**

- **Heard words** become user messages.
- **Speech** goes back as the speak calls that made it, never as assistant
  content. The prompt says content is never heard, and a history full of
  content answers would teach the model otherwise. Each speak call carries
  the heard words, and its result says whether the person heard it all, cut
  it off, or is still hearing it.
- **Other calls** made back to back go in one assistant message, as the model
  made them.
- **Each result** is a tool message named by its tool, which is how
  `/api/chat` matches the two. A result that is not `ok` states its outcome
  first, so a timeout is not read as an answer.
- **Arguments a model wrote as broken JSON** go back as `{}`, because
  resending them would fail the whole ask.

The prompt is unchanged, so its fingerprint is too.

## Alternatives rejected

- **Have the engine loop on its own results.** The engine would have to run
  tools, or wait on the session's, and replay could no longer tell which ask
  produced which call. The session already owns dispatch and the journal.
- **Keep a running message list in the session.** That is a second copy of
  the conversation beside the log, and it drifts the first time a detached
  result, a barge-in or a migration writes to one and not the other.
- **Send speech back as assistant content.** This is shorter, but it shows
  the model answering in the channel the prompt forbids, and measured models
  copy their history (DefaultPrompt's notes).
- **Wait for speech to finish before asking again.** This would add the
  length of "Let me check." to every answer. The follow-up streams while the
  acknowledgement plays and queues behind it.

## Forecloses

A turn with tool calls now costs one more model ask than it did, and the
first audio of the answer waits on it. ADR-0035's `speech_started` measures
that wait as before. The round cap ends a model that never stops calling
tools, but nothing is said to the person when it fires.

Replay's re-runs (`internal/rerun`) still ask a recorded turn with its text
alone and execute nothing, so they now compare against the turn's first ask
only. Re-running the follow-ups would mean feeding them the recorded results,
which is what `journal.Overrides.ToolResults` was built for. It is not wired
yet.
Confirmation gates (SPEC §6) build on this: a "yes" now reaches a model that
remembers the question. The models tier asks a real model to answer from the
garage door's state (`TestARealModelAnswersFromTheResultItIsGiven`). That
test was not run where this was written, which has no Ollama endpoint.
