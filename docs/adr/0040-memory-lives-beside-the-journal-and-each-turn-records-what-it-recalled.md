# 0040. Memory lives beside the journal, and each turn records what it recalled

- **Status:** accepted
- **Source:** SPEC §5, §8 · ADR-0016, ADR-0034, ADR-0037

SPEC §5 promised `remember` and `forget` tools, recall into each
conversation, and memories private unless shared. The schema declared
`remember`, but nothing ran it, and nothing enforced `scope: person`.
Now a person's memories outlive the conversation and the daemon, and the
model is told them on every turn.

```go
import (
	"context"

	"github.com/teaganglenn/chorus/internal/memory"
)

// Teagan is told the oat milk and the wifi password Alice shared, never the
// surprise party Alice kept to herself.
func whatTeaganRecalls(ctx context.Context, s memory.Store) ([]memory.Memory, error) {
	return s.Recall(ctx, "teagan", memory.RecallLimit)
}
```

**A guest cannot use a person's tools.** A call to a tool declared
`scope: person` is refused when the session has no identified speaker. The
result is `{"error":"unidentified_speaker"}`, and the refusal comes before
any confirmation. A tool declaring `unknown_speaker: guest_fallback` runs
for the guest instead. The executor learns whose call it is from
`session.CallerFrom`, never from the arguments. A model that could name the
person could name anyone.

**The store sits beside the journal, as curation verdicts do (ADR-0034).**
`memory.PgStore` keeps one row per memory in the journal's database:

- whose it is
- the fact
- whether it is shared
- the conversation and call that made it

`forget` deletes the row. Teagan chose this over also redacting the log. The
`remember` call stays in the journal as history, so a forgotten fact is
never recalled again but is still in the trace it came from. A person can
forget only their own memories. Asking to forget anyone else's gets the
same answer as an id nobody has.

**Each turn records what it recalled.** Before the first ask of an utterance,
the session recalls the speaker's memories. It writes `memory_recalled` when
they differ from what the log last recorded. The model is then asked with the
log's copy, so a replay asks with exactly what the turn was given, whatever
the store holds by then.

A harvested pair carries what its rejected turn recalled, and the export
writes it as `meta.recalled`. A correction trained without the memory it
answered from teaches a model to state facts it was never given.

A turn that cannot recall fails. The store is the journal's database, so a
turn that went on without it would be answering over a log in trouble.

**What is recalled.** The speaker's own memories and everyone's shared ones,
newest first, at most `memory.RecallLimit` (20). A guest recalls nothing.
Ollama puts them in the system message after who the model is speaking with.
Each line starts with the id `forget` takes, then the fact quoted as it was
said. A memory somebody else shared says whose it is. A re-run of a turn
(SPEC §9.2) is asked with the memories that turn recorded.

## Alternatives rejected

- **Derive memory from the log.** It is the purest reading of SPEC §8, but
  `forget` would then need redaction in an append-only log, and every turn
  would scan every conversation the person ever had.
- **Fetch memories inside the engine on each ask.** The turn would not be
  replayable: the model's input would depend on the store at replay time.
- **Let the model say whose memory it is.** `remember` would take a
  `person`, and anything that can speak to the model could write into
  anyone's memory.

## Forecloses

- **The speaker is sticky within a conversation.** SPEC §5 flips attribution
  only on a confident match to someone else. A guest who speaks into Teagan's
  conversation without matching anyone is still Teagan to the session. That
  guest is told Teagan's memories and can remember as Teagan.
- **The review UI does not show recalled memories yet.** The pair and the
  export carry them; the inspector does not.
- **A memory is a person's words in the system message.** Quoting keeps a
  newline from forging a line of its own, and the heading calls the facts
  facts, not instructions. A model can still obey one. What a household
  member could inject this way, they could say to it directly. A shared
  memory reaches the others too, and a call that matters is held for their
  yes (ADR-0038).
- **No relevance ranking yet.** Recall gives everything up to the limit,
  newest first. A household past twenty memories a person loses its oldest
  from the prompt until recall ranks by relevance.
- **"What did I ask yesterday" is not this.** Only facts the person asked to
  keep are remembered. The per-person summaries SPEC §5 also names are
  ADR-0042.
- **The prompt is unmeasured.** The models tier asks qwen3:14b to remember
  the oat milk, to answer from it, and to forget it by its id. It has not run
  where this was written, which has no Ollama endpoint.
