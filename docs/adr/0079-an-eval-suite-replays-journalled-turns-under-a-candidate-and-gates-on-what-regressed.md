# 0079. An eval suite replays journalled turns under a candidate, and gates on what regressed

- **Status:** accepted · refines [ADR-0059](0059-every-re-run-is-kept-beside-the-journal-and-its-tool-schema-is-the-reviewers-to-edit.md) and [ADR-0071](0071-an-export-row-carries-the-context-its-turn-ran-under.md)
- **Source:** SPEC §8, §9.2, §11, §12, §13, §14 · ADR-0007, ADR-0037, ADR-0038, ADR-0059, ADR-0071, ADR-0075

SPEC §13 records the persona's version on every event "so persona changes
are A/B-testable against replayed traces", and ADR-0071 keeps the text
behind every version. Replay could ask one conversation again under an
edited prompt, but nothing could ask a chosen set of turns the same way
twice, score the answers, and say whether a change made things worse. That
was §14's Phase 3 item. `internal/evalsuite` and `cmd/chorus-eval` build it:
a checked-in suite names turns by their journal reference and says what a
good answer does, and the runner scores a candidate beside a baseline and
exits 1 when the candidate regresses.

```go
import "github.com/teagan42/chorus/internal/evalsuite"

// A candidate fails on any regressed check, or a fall of more than 0.05.
var gate = evalsuite.DefaultGate
```

## The suite

A suite is YAML, read strictly: an unknown field is an error, so a
misspelt expectation cannot pass as no expectation. It is checked in Go,
as `mcp.yaml` and `devices.yaml` are. `schema/` holds the event taxonomy
and the tool registry, the two formats whose drift corrupts data
(CONTRIBUTING §2). A case names its turn as the review UI does,
`conversation/seq`, the seq of the turn's utterance. Its expectations are:

- tools called or not, with arguments held as a subset or matched by
  pattern;
- phrases said or not, and a word count;
- a first-speech budget;
- whether it asks for a yes;
- that no call names an entity the log never showed;
- an optional reference answer, scored by word overlap, which is a check
  only once the case sets a floor.

A case's labels must be SPEC §9.2's vocabulary.

Every problem is reported at once, each with its case: a ref with no seq,
a tool the registry does not declare, a case that expects nothing. A ref
the journal does not hold is refused too, saying what is there and which
seqs are turns. A suite is a gate, and a gate that quietly scored a typo
as a pass would be worse than none.

## What a case is asked

A case's turn is read by `rerun.Turns`, the same reading Replay makes: the
speaker, the room, the time it was heard, what it was told it remembers,
and its first ask as the take it recorded. `rerun.Run` is now `rerun.Ask`
with `rerun.Input`, behaviour unchanged, so an eval and Replay cannot
drift in what they ask.

**One difference is deliberate.** Replay asks a turn on its own
(ADR-0037). An eval also sends the dialogue the reducer folded through the
turn's utterance, which is what the live session's first ask was sent.
`rerun.Turn` keeps it, and Replay still leaves it out. "Just close it" and
"yes" mean nothing alone, and a persona change judged on turns stripped of
their conversation is judged on questions nobody asked.

The baseline's context is resolved by the labels the turn recorded:
the prompt's text, and the tools as the wire offered them, read back with
`ollama.ParseToolSchema`. **A label nothing holds is never guessed**
(ADR-0071). The case is skipped as `context_missing`, naming the half that
is gone, and never failed. Every turn that offered an MCP server's tools
is one (ADR-0075).

## The runner

`evalsuite.Engines` builds an engine for a model, prompt, tool set and
seed. It is Replay's engine factory with a seed, and `evalsuite.Factory`
adapts that factory's shape unchanged. `cmd/chorus-eval` keeps a map of
providers, Ollama's and the demo's, so another provider is one entry.
Ollama's engine takes no seed, so the seed is only the run's number there.

Each case is asked N times a side. It is one ask, as Replay's, and nothing
runs: the engine never executes a tool (ADR-0037). First speech is timed
from the ask to the first streamed words, on an injected clock, so the
hermetic tier measures exactly what a script says.

An engine that cannot be built or asked stops the run with exit 2: a model
that is down is a broken setup, not a regression. A stream that breaks
mid-answer is a failed run, scored and reported, since it may be the
model's fault.

**Takes are cached** under a key of the provider, the turn and the input
it is asked, the model, the prompt's and tools' text and versions, and the
seed. Scoring again, adding a case or raising the repetitions asks only
what is new. A failed run is never cached, and a torn cache file is asked
again rather than trusted.

## Scoring and the gate

Each check's pass rate is its passes over the runs it scored. A latency
budget on a recorded take is unscored, not passed. A side's score is the
mean pass rate over the checks both sides scored, so neither side gains
from a check the other cannot be judged on. A check regresses when its
rate falls by half its runs or more, so one run against one counts and one
bad run in three is variance. Variance is shown as each check's k of n and
a count of flaky checks.

The candidate fails when more checks regress than allowed (none by
default), when its score falls by more than the threshold (0.05), or when
no check was compared at all. Exit 0 is a pass, 1 a regression and 2 a
run that could not happen. A gate can tell the last from the second.

The baseline is either the recorded versions asked again, the default,
or the recorded takes asked of nobody. The first compares two samples from
today's endpoint. The second is free, and is the only one possible once
a recorded model is gone.

## Drafts from the review UI

`chorus-eval draft` writes a suite from what reviewers judged. Each
exemplar expects the calls it made, each correction and each accepted
barge-in pair is a reference, and *too slow* is a budget. It is a draft:
the person tightens it and checks it in, so the suite under review is
the one that runs, and a reviewer's later click never changes a gate
that already passed.

## The seed suite

[`eval/household.yaml`](../../eval/household.yaml) is eight cases over the
demo household's Thursday. `internal/evalsuite/evalfake` answers each turn
as the day recorded it, unless a script says otherwise for a prompt, so
CI runs the whole path with no model. A take that called a tool the
engine is not offered calls nothing and says it cannot, as
`household.Model` does. Cases ask what today's build should do: the album
search expects no `media_search` while media is deferred (SPEC §14), so
`task eval:demo` exits 0. The tests prove exit 1 with candidates that
break a case, such as a tool set without `timer_cancel`.

## Alternatives rejected

- **A CUE schema for the suite.** The constraints are Go's to check
  anyway: whether a tool is declared, whether a pattern compiles, whether
  a ref is in the journal. `schema/` holds the two formats whose drift
  corrupts training data, and this is neither.
- **Select cases from curation labels at run time.** The same suite
  would test different turns tomorrow, a reviewer's click would change a
  gate, and a pull request could not show what it was judged on.
- **Judge speech with a model.** That brings a second sampled model into a
  gate meant to be repeatable. Word overlap is crude, but it says the
  same thing twice; a floor is opt-in.
- **Ask the follow-ups with the recorded results.** The recorded results
  answer the recorded calls. A candidate that calls something else would
  be told results that are not its own, and `journal.Overrides.ToolResults`
  is still not wired (ADR-0037).
- **Keep results in the journal or beside it.** The journal records the
  runtime (SPEC §8). A score is a file a pull request carries, and the
  cache is a cache.

## Forecloses

- Only first asks are judged, so a regression in how the model uses a
  tool's result is invisible here.
- `asks_confirmation` is a question mark and no forged confirmation; a
  question asked without one is missed.
- The entity check knows Home Assistant's domains and what the log showed.
  An entity the house has, but the conversation never named, reads as
  invented unless the case lists it.
- A suite names the household's conversations, so a household's suites
  live with its configuration, not in this repository.
- Ollama runs are unseeded, so a cached run is a sample, not a
  reproduction; clearing the cache draws again.
