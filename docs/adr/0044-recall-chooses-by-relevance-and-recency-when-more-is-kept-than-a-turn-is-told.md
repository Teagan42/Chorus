# 0044. Recall chooses by relevance and recency when more is kept than a turn is told

- **Status:** accepted
- **Source:** SPEC §5, §7, §8, §11 · ADR-0040, ADR-0043

SPEC §5 says memories and summaries are "injected by relevance". ADR-0040
and ADR-0043 injected the newest: twenty memories, and five conversations
from the past week. Teagan told the house the garage code in August. Two
dozen memories later, "what's the code for the garage" was answered without
it, because the code was the oldest thing kept. Now, once a person has
more than a turn is told, recall chooses by how close each one is to what
was just said, as well as how recent it is.

```go
import (
	"context"
	"time"

	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/session"
)

// What Teagan is told when asking for the garage code, with an embedding
// model ranking: the code, however old, and the newest of the rest.
func garageCode(ctx context.Context, s memory.Store, e memory.Embedder, now time.Time) (session.Recollection, error) {
	r := memory.Recaller(s, memory.RecallConfig{Embedder: e})
	return r.Recall(ctx, session.Ask{
		Person: "teagan", ConversationID: "conv-garage-0853", Now: now,
		Words: "what's the code for the garage",
	})
}
```

**Relevance is an embedding model's.** `OLLAMA_EMBED_MODEL` names one on
the turn engine's endpoint (`nomic-embed-text`, say), called through
`/api/embed` and kept resident as the turn model is. Unset, a turn is told
the newest, as before, and chorusd says so at startup. Recall is asked with
the turn's words: the transcript the turn is answering.

**Recency and relevance are fused, not thresholded.** The candidates are
ranked twice: by age, and by cosine to the words. Each one's score is the
sum of the reciprocals of its two ranks, offset by 60 (reciprocal rank
fusion), and the best twenty memories and five conversations are kept.
Whatever was chosen is told newest first, as before. Nothing needs a
similarity threshold, which would differ from one embedding model to the
next and could not be measured here. Yesterday's conversation stays near
the top on recency alone, so "what did I ask you yesterday", whose words
are about no conversation, still gets it. An old memory climbs only when it
is about what was asked.

**Conversations come from the month, not the week.** With a ranker, the
candidates are the person's summaries from the past thirty days, which is
how long one is kept. Without one, they are the past week's, as ADR-0043
set. When everything fits (twenty memories or fewer, five conversations or
fewer), nothing is embedded and all of it is told.

**Ranking can only add.** A turn whose ranking fails or runs out of time is
told the newest, exactly what it would be told without an embedding model,
and the daemon logs why. The bound is 500 ms, from SPEC §11's ~700 ms to
first audio. A turn usually embeds one short text: what is kept is embedded
once, cached by its text, and only the words are new. The first turn after
a restart embeds what the person has kept, 32 at a time, in the background.
A turn that cannot wait for it is told the newest, and the embedding
carries on, for up to five minutes, so the next turn is ranked. A minute
was not enough to load `nomic-embed-text` cold on the household's Ollama.

**The log says what chose.** `memory_recalled` gains `ranked_by`, the
embedding model, when one chose. It is absent when the turn was told the
newest, and a change between the two is recorded like any other change in
what a turn is told. A replay is told what the turn was told, whichever
chose it, and never embeds anything.

## Alternatives rejected

- **A similarity threshold.** "Tell anything above 0.6." The number is a
  property of the embedding model, not of the household. It would need
  measuring on each model, and changing a model would silently change what
  is recalled.
- **Rank by relevance alone.** "What did I ask you yesterday" is about no
  conversation in particular, so its nearest neighbours would be whatever
  happens to share its words. Recency has to keep a vote.
- **Store the vectors in Postgres (pgvector, or a `real[]` column).** A
  household keeps hundreds of texts, which a process can hold and compare
  in microseconds. A column would need a migration, a re-embed whenever the
  model changes, and the pgvector extension in every database chorusd runs
  against. That is a cost for a household of thousands.
- **Ask the turn model to pick.** One more full model call before the first
  word, on every turn.
- **Lexical ranking (BM25, Postgres full text).** It needs no model, but
  "how do I get into the garage" shares no word with "the garage door code
  is 4512" except *garage*, and "can Alan have the satay" shares none with
  "Alan is allergic to peanuts".

## Forecloses

- **The first turn after a restart can be slow, or unranked.** A person
  with hundreds of memories waits for them to be embedded, up to the 500 ms
  bound. Past it the turn is told the newest, and the next turn is ranked.
  So is a turn whose embedding model was unloaded, until it loads.
- **The embedding model is one more resident model.** `nomic-embed-text`
  is about 270 MB beside the turn model on the same GPU.
- **Words are embedded as said, with no task prefix.** Some models (nomic's
  among them) rank better given `search_query:` and `search_document:`
  prefixes. That is a per-model choice, not made here.
- **Measured on the household's Ollama, once.** The models tier asks
  `nomic-embed-text` to bring back four memories, each the oldest of two
  dozen, from questions that share few words with them ("can Alan have the
  satay" for the peanut allergy). It did, once the model had loaded; its
  first, cold request ran past the one-minute bound this ADR first set. The
  same run asked qwen3:14b for the garage code it was given. In one run of
  five it reasoned that it needed no tool to say what it already knew, and
  wrote `speak` and the code as plain content, which nobody heard. The
  prompt now says an answer already known is still a speak call. That was
  not enough, so ADR-0046 speaks such content when the turn calls nothing.
