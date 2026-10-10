# 0043. Each conversation is summarized for the people in it, and each turn is told the time

- **Status:** accepted
- **Source:** SPEC §5, §8 · ADR-0022, ADR-0034, ADR-0040

SPEC §5 promised a rolling summary per person beside the explicit memories.
ADR-0040 shipped the explicit half, and asking "what did I ask you
yesterday" still got a blank. The model had no record of earlier
conversations, and no idea what day "yesterday" was. Now every conversation
is summarized when it ends, and each turn is told the time and the person's
recent conversations.

```go
import (
	"context"
	"time"

	"github.com/teaganglenn/chorus/internal/memory"
)

// What Teagan is told on Friday morning: the last week's conversations,
// newest first, except the one Teagan is in.
func teagansWeek(ctx context.Context, s memory.Store, now time.Time) ([]memory.Summary, error) {
	return s.Summaries(ctx, "teagan", "conv-kitchen-0853", now.Add(-memory.SummaryWindow), memory.SummaryLimit)
}
```

**Every turn is told the time.** The time is when its utterance was logged
(`State.HeardAt`), rendered in the household's zone. The daemon reads the
zone from `TZ` and logs it at startup. A replay is told the time the turn
was heard, not the time the replay runs.

**A conversation is summarized when it ends, not when it moves.** A close
for any reason but `migrated` snapshots the conversation and writes the
summary in the background, after the session is gone. The snapshot is taken
before the conversation is released, so a person waking again within the
migration window resumes it only afterwards. That resumed conversation's
own end writes a newer summary. The summary goes to the same Ollama model with its own
prompt, no tools and no streaming. The model is shown the conversation as
it happened: each utterance quoted, with who said it, which is why a heard
dialogue entry now carries its speaker. It writes a sentence or two on who
asked for what and how it turned out.

The summary is kept for each identified person who spoke or woke the
device. Nothing is written for a conversation with nobody identified, or
with nothing said. The daemon waits for summaries in flight before it
exits, and each gets `session.DefaultSummaryTimeout` (60 s) on the injected
clock.

**The log says whether it worked.** `conversation_summarized` records whom
it was for and the summary, or why there is none:

- the model failed or timed out
- it wrote nothing
- the store refused it

A replay cannot regenerate the model's words, so the summary is recorded
in full, as a completion is.

**Summaries are private.** Everyone a summary is kept for was there, so
nothing is disclosed to them. Nobody else is ever told it. They sit beside
the memories, one `conversation_summaries` row per conversation and
person. A conversation resumed within the migration window and ended again
replaces its summary. An older summary arriving late does not.

**What is recalled.** Each turn gets the person's five most recent
summaries from the past seven days. The conversation in progress is left
out, since its dialogue is already in the prompt. Each summary carries the
time of the last thing heard in that conversation. Keeping a summary
prunes that person's summaries more than thirty days older than it.
`memory_recalled` records the summaries beside the memories, so a replay
is told what the turn was told. Logs from before this have no
`summaries_json`, and recall none.

Ollama puts the time after who the model is speaking with. The recent
conversations go after the memories, each stamped with its day and time
and quoted as memories are. A re-run is asked with the same time and
summaries. A harvested pair carries them, exported as
`meta.recalled_conversations` and `meta.heard_at`.

## Alternatives rejected

- **One rolling summary per person, rewritten at each conversation's end.**
  It is the literal reading of SPEC §5. But each rewrite compounds the
  model's errors. Two conversations ending at once would race on one row.
  And a conversation two people were in would rewrite both their
  summaries from one call.
- **Summarize from the log on demand.** Every turn would scan every
  conversation the person had that week, and ask the model to summarize
  them, before saying a word.
- **Use the wall clock at ask time.** A replay would be told the day it
  runs, and "yesterday" would mean a different day every time it ran.
- **Give the summary to everyone in the household.** It may hold another
  person's memories, recalled into a conversation they were not in.

## Forecloses

- **Relevance does not choose them yet.** The newest five from the week are
  told, whatever was asked. ADR-0044 ranks memories and summaries by
  relevance.
- **A guest's words reach the summary.** An unrecognised voice in Teagan's
  conversation is in Teagan's summary, as it was in Teagan's conversation.
- **A summary costs a model call per conversation.** It runs after the
  people have walked away, on the turn model held resident. It is still
  load on a GPU the next wake may want.
- **The summary prompt is part of the prompt version.** Every event's
  `prompt` fingerprint changes with this release, without the turn prompt
  changing.
- **The prompt is unmeasured.** The models tier asks qwen3:14b to summarize
  the garage door, to answer "what did I ask you yesterday" from a summary,
  and what day it is. It has not run where this was written, which has no
  Ollama endpoint.
