# 0052. Labels and promoted re-runs live beside the journal, and make pairs on read

- **Status:** accepted
- **Source:** SPEC §8, §9.2 · ADR-0026, ADR-0034

SPEC §9.2 asks for eight labels, a free-text "what it should have done",
and edit-and-replay, but until now the only judgment a reviewer could record
was a verdict on a harvested barge-in. Labels and promoted re-runs are a
person's revisable annotations of the log, so ADR-0034's reasoning puts them
where it put verdicts: in `internal/curation`, in their own upserted tables
beside the journal, keyed by the turn's utterance seq, which is how the
harvester and Replay already name a turn.

```go
import "github.com/teagan42/chorus/internal/curation"

// A turn with a fault and what it should have done is a pair; an exemplar is not.
func makesAPair(a curation.Annotation) bool { return a.Faulted() }
```

**An annotation's pair is derived.** A turn labelled with any fault (every
label but *good — exemplar*) and given a note becomes a pair with source
`annotation`: the recorded take rejected, the note chosen. Nothing stores
the pair. Curate cuts it from the turn on every read (`harvest.Turn.Pair`),
as ADR-0026 does for barge-ins, so the rejected side can never drift from
the log. Changing the note, or taking the last fault off, deletes the
pair's verdict: the reviewer judged a chosen side that no longer exists.

**A promoted re-run is stored whole.** The journal never held a re-run's
take, so `curation_promotions` keeps its speech, the calls it would make,
the versions it ran under and the edited system prompt itself. The pair
(source `replay`) is still cut from the turn on read; only its chosen side
comes from the table. The versions are the server's, recomputed from the
model and prompt the page sends back, not the page's word. Promoting is the
verdict, so the pair lands accepted, and undo in Curate returns it to
unreviewed.

The export carries `meta.labels` on an annotation's row, and
`meta.chosen_versions` and `meta.chosen_calls` on a replay's, so a trainer
can filter on what made a pair and attribute both sides of it.

## Alternatives rejected

- **Store annotation pairs as rows.** A copy of the rejected turn would go
  stale if the log were ever re-read differently, and would make the
  annotation and its pair two things to keep in step.
- **Store every re-run.** Most re-runs are exploration. Only the take a
  reviewer chose is a fact about the dataset; the rest can be asked again.

## Forecloses

One annotation and one promotion per turn: a second promotion replaces the
first, and there is no history of either (the same limit ADR-0034 accepted
for verdicts). A chosen side that is a tool call rather than speech exports
its calls in meta, not in the chosen message, until the export grows a
tool-call message shape.
