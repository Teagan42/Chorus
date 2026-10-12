# Eval suites

A prompt change, a new model or an edited tool description is judged on the
household's own traffic before it ships. An eval suite is a checked-in list
of journalled turns, each with what a good answer does. `chorus-eval` asks
each turn again under the candidate and under the versions the journal
recorded, scores both sides, and exits 1 when the candidate regresses
(SPEC §13, [ADR-0079](adr/0079-an-eval-suite-replays-journalled-turns-under-a-candidate-and-gates-on-what-regressed.md)).

It asks the way the review UI's Replay does: the same turn, rebuilt from
the log, with the system prompt and tools it ran under found by the labels
it recorded (ADR-0071). Nothing is executed, and nothing is written to the
journal.

## A suite

```yaml
name: front-door
repetitions: 3
cases:
  - id: door-asks-first
    ref: conv-1858-kitchen/2        # conversation / the utterance's seq
    from: exemplar                  # hand, exemplar, annotation, barge-in, replay
    labels: [exemplar]              # SPEC §9.2's vocabulary
    note: Book club at the door. Unlocking waits for Teagan's yes.
    expect:
      tool_called:
        - tool: ha_call_service
          args: {domain: lock, service: unlock, entity_id: lock.front_door}
      tool_not_called:
        - tool: timer_start
      says:
        contains: [front door]
        not_contains: [unlocked]
        max_words: 8
      first_speech_ms: 1500
      asks_confirmation: true
      no_invented_entities: true
      reference: Unlock the front door?
      min_reference_overlap: 0.5
```

[`eval/household.yaml`](../eval/household.yaml) is a whole suite over the
demo household's Thursday.

Each field under `expect` is one check or more, scored pass or fail on every
run:

| Field | Passes when |
|---|---|
| `tool_called` | each entry has a call to its tool whose arguments hold every value in `args` (objects as subsets; key order and extra keys don't matter) and match every regular expression in `args_like` |
| `tool_not_called` | no call matches the entry. An entry with no `args` matches any call to the tool |
| `says.contains`, `says.not_contains` | the phrase is, or is not, in what the answer said, ignoring case and spacing. One check per phrase |
| `says.max_words` | the answer is at most this many words |
| `first_speech_ms` | the engine streamed its first words within this many milliseconds of the ask. A turn that never spoke misses it. Not scored on a recorded take, which has no stream |
| `asks_confirmation: true` | the answer asks a question, and no call carries a confirmation the log never handed out (ADR-0038) |
| `asks_confirmation: false` | the answer asks no question |
| `no_invented_entities` | every Home Assistant entity a call names is one the conversation's results named through the turn, one its calls named that did not fail, or one `known_entities` lists |
| `min_reference_overlap` | the words the answer shares with `reference`, as an F1 from 0 to 1, reach the floor. Without a floor the overlap is reported and decides nothing |

A suite that cannot be trusted does not run. A misspelt field, a tool the
registry does not declare (an MCP tool is named `mcp_<server>_<tool>`,
ADR-0075), `speak` used as a tool, a bad pattern, a case that expects
nothing, or a `ref` the journal does not hold, each stops the run with the
case and the reason. A `ref` that points at something other than an
utterance is told what is there and which seqs are turns.

### Writing a case from the review UI

1. Find the turn. In [Replay](reviewui/README.md#replay), each turn's row
   is anchored `#turn-<seq>`; on a conversation's page, the utterance's row
   is `#seq-<seq>`. The ref is the conversation id, a slash and that seq:
   `conv-1858-kitchen/2`.
2. Write down what a good answer does, not what this one said. A reviewer's
   correction, or a barge-in's chosen side, makes a good `reference`.
3. Run the suite once against the build you have, and read what fails. A
   case that fails under the recorded versions as well is a known fault the
   candidate is meant to fix, like the demo's long forecast.

`task eval:draft -- -name thursday > thursday.yaml` starts a suite from what
reviewers already judged: every turn marked *good — exemplar* expects the
calls it made and its words; every turn with a fault label and a
correction, and every accepted barge-in pair, takes the reviewer's words as
its reference; a turn labelled *too slow* gets a first-speech budget. Read
each case and tighten it before you check it in.

A suite names the household's conversations, so it lives wherever the
household keeps its `devices.yaml`. The repository's `eval/` holds only the
demo's.

## Running a suite before changing the persona

```sh norun
# Start from the prompt this build sends, and edit it.
go run ./cmd/chorus-eval -print-prompt > persona.txt

# Score it against the suite, three runs a side.
task eval -- thursday.yaml -prompt persona.txt -reps 3 -markdown eval.md
```

`chorus-eval` reads the journal at `CHORUS_POSTGRES_DSN` and asks the model
at `OLLAMA_URL`, the endpoint `chorusd` uses. The candidate runs this
build's prompt and offered tools unless `-prompt` or `-tools` names a file;
`-print-tools` writes the tools in the shape Replay's schema editor holds,
to start a `-tools` file from. `-model`, or `OLLAMA_MODEL`, picks the
candidate's model; unset, each turn's recorded model is asked.

The baseline is one of two:

- `-baseline rerun`, the default: each turn asked again under the model,
  prompt and tools it recorded. It is the fair comparison, since both sides
  are sampled from the same endpoint today. `-baseline-model` replaces a
  recorded model the endpoint no longer serves.
- `-baseline recorded`: the take the journal recorded, asked of nobody.
  Cheaper, and first speech is not scored on it.

The terminal gets a table of every check with each side's pass rate. Add
`-json report.json` for a machine, and `-markdown eval.md` for a pull
request: the summary, then only the checks that moved or fail on the
candidate.

```text
CASE                     CHECK                                              BASELINE  CANDIDATE
door-asks-first          calls ha_call_service {"domain":"lock",…}          1/1       1/1
                         asks for a yes                                     1/1       1/1
zeppelin-searches        calls media_search like {"query":"(?i)zeppelin"}   1/1       0/1        regressed

score  baseline 0.955 · candidate 0.909 · Δ -0.045 over 22 checks
REGRESSED: 1 check regressed, more than the 0 allowed
```

`task eval:demo` runs the seed suite over the household's Thursday with a
scripted model, no database and no model needed. It exits 1, as above:
today's registry defers `media_search` (ADR-0060), so a candidate on it
cannot look for Alice's album. That is the gate doing its job.

## Scoring and the gate

Each check's pass rate is the runs it passed over the runs it scored. A
side's score is the mean pass rate over the checks both sides scored. A
check *regresses* when its pass rate falls by half its runs or more, and
*improves* when it rises as far: one run against one is a whole run, and
one bad run in three is variance, shown in the score and counted as flaky,
not a regression.

The candidate fails when more checks regress than `-max-regressions`
allows (0 by default), when its score falls below the baseline's by more
than `-threshold` (0.05), or when no check was scored on both sides.

| Exit | Means |
|---|---|
| 0 | the candidate held its ground |
| 1 | the candidate regressed beyond the gate |
| 2 | it could not run: a bad suite, a ref the journal lacks, no model, an engine that would not answer |

## Repetitions, seeds and the cache

`repetitions` in the suite, or `-reps`, asks each case that many times a
side. Each run is cached in the user cache directory (`~/.cache/chorus-eval`
on Linux), or `-cache`, by the provider, the turn and what it is asked, the
model, the prompt's and tools' text and versions, and the run's number, so
scoring again, or adding
a case, asks only what is new. `-no-cache` asks everything. A run the
model broke off mid-answer fails every check and is never cached. The run's
number is the seed an engine is built with; Ollama's engine takes none, so
its runs vary as the model samples.

## Skipped cases

A turn whose prompt or tools no build archived cannot be asked as it was:
it is reported `skipped: context_missing: prompt` or `tool_schema`, as the
dataset export says it, and never counted as a failure. Every turn that
offered an MCP server's tools is one (ADR-0075).

## What it does not do

- It asks each turn's first ask, as Replay does. A follow-up ask, fed the
  calls' results, is not asked: the recorded results answered the recorded
  calls, not the candidate's.
- It runs no tool, so it never knows whether a call would have worked; the
  entity check reads only what the log showed.
- `asks_confirmation` looks for a question mark, and `reference` counts
  shared words. Neither understands an answer; a model judging one is not
  built.
- First speech is the engine's, from the ask to its first streamed words.
  The voice's own latency (ADR-0035) is not in it.
