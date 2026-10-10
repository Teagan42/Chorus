# 0054. A pair carries what each side called beside what it said

- **Status:** accepted
- **Source:** SPEC §4.1, §9.2 · ADR-0003, ADR-0052

ADR-0052 kept pair text speech-only, so a promoted re-run had to say
something and its calls rode in meta. That left the household's commonest
mistake untrainable: most first asks are tool calls, and a model that picked
the wrong one politely could not be corrected. Now each side's assistant
message carries `tool_calls` beside its content, in the function-call shape
the model's template already emits with content (SPEC §4.1). Speaking stays
the content; `speak` is never a call in pair text (ADR-0003).

```go
import (
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// What the export writes beside each side's speech, or false for speech alone.
func sides(p harvest.Pair) (rejected, chosen []journal.Call, withCalls bool) {
	return p.TextCalls()
}
```

**Whose calls each side carries.** The rejected side carries the calls its
turn made, other than speaking: the whole turn for a barge-in or a note, as
its speech is, and the first ask for a replay, as ADR-0052 reads it.

- A replay's chosen side carries the re-run's calls, and may be calls alone:
  a re-run that checks the sensor that answers, and says nothing until it
  has, is a chosen side. Curation now refuses only a take that neither says
  nor calls anything (migration 0003).
- A barge-in's correction and a note are about what was said, so their
  chosen side keeps the turn's calls. Leaving them off would teach the model
  to stop calling tools whenever it was told to say less.
- A *wrong tool / args* note says the calls were wrong without saying which
  were right. Its pair carries no calls on either side and says
  `meta.speech_only`, rather than guessing.

Curate shows each side's calls under its speech, from the same
`harvest.Pair.TextCalls` the export uses, so what a reviewer accepts is what
ships. Malformed arguments are kept as the model wrote them, as a string.

## Alternatives rejected

- **Allow an empty chosen side and keep calls in meta.** A loader reads only
  the messages, so the pair would teach silence over the recorded speech,
  which is worse than no pair.
- **A tool-call-only pair source.** Speech and calls come from one
  generation (ADR-0003); splitting them into separate pairs would train each
  without the other's context.
- **Let a note author calls.** A reviewer writing JSON arguments by hand is
  slower and less faithful than re-running the turn under a fixed prompt,
  which Replay already does.

## Forecloses

Rows exported before this change carry no `tool_calls`; a training run that
mixes old and new exports sees calls on only some rows. A *wrong tool* note
trains nothing about tools until the reviewer re-runs and promotes the turn.
