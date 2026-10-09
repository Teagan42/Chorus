# Review UI

`cmd/reviewui` is where a person reads what the household's satellites did and
turns the barge-ins worth learning from into a preference dataset (SPEC §9).
It is Go and htmx over the same Postgres journal and blob directory `chorusd`
writes, and it is a reader: every row on every screen is derived from the log
on request, never copied out of it (SPEC §8, ADR-0007). The one thing it
writes is a reviewer's verdict on a pair, and that lives in its own table
beside the journal, not in it (ADR-0034).

Inline audio is the point. A voice assistant cannot be judged from
transcripts (SPEC §9.2), so every clip on every screen plays, and a truncated
turn plays only what the speaker actually reached.

## Running it

It needs the journal database and the blob directory `chorusd` records into.
Both schemas, the journal's and the curation table's, migrate at startup.

```sh norun
task db:up                     # the journal
task reviewui                  # go run ./cmd/reviewui, loads .env
task reviewui -- -addr :9090   # somewhere other than :8080
```

| Variable | Required | What for |
|---|---|---|
| `CHORUS_POSTGRES_DSN` | no | The journal. Defaults to the docker-compose database. |
| `CHORUS_BLOB_DIR` | yes | The audio the journal refers to; served to the browser as WAV. |
| `OLLAMA_URL`, `OLLAMA_MODEL` | no | What Replay re-runs turns against. Unset, Replay shows the recorded turns and says it has nothing to ask. |

These are the same variables `chorusd` reads, so the `.env` copied from
`.env.example` already serves both. `/` redirects to Curate.

## The screens

The header runs left to right in the order a reviewer usually works, and
badges the number of harvested pairs nobody has judged yet.

| Screen | Route | Answers |
|---|---|---|
| [Browse](#browse) | `/conversations` | What happened today, on which satellite, to whom? |
| [Triage](#triage) | `/queue` | Which of it is worth a person's time? |
| [Review](#review) | `/review` | Was this barge-in cut where the speaker actually stopped? |
| [Replay](#replay) | `/replays` | Would a different prompt or model have done better? |
| [Curate](#curate) | `/curate/pairs` | Does this pair go in the dataset, and as which answer? |
| [Export](#export) | `/export` | What ships, and what is being held back? |

### Browse

![Browse](browse.png)

One lane per satellite across the day, `?day=YYYY-MM-DD` for any other.
Each block is a stretch of one conversation on one satellite, so a person
walking from the kitchen to the office shows up as one conversation in two
lanes. A block takes the colour of its most important signal: a barge-in
first, since it is the training signal, then a failure, a repeated ask, a
speaker flip. Ticks mark wakes rejected at stage two (SPEC §9.3), which
opened no conversation and so live in the satellite's own `device:` log.

![Conversation](conversation.png)

`/conversations/{id}` is one conversation's journal, event by event, with the
audio each event refers to. Every row carries a `#seq-N` anchor, which is how
Triage links straight to the event that raised a signal.

### Triage

![Triage](triage.png)

Every conversation in the household scanned for four signals
(`internal/triage`), newest first, filterable by tab:

| Signal | Raised when | Opens |
|---|---|---|
| barge-in | someone interrupted the assistant mid-turn | the pair in Review |
| failure | a tool errored or timed out (ADR-0008), the model finished with an error, or the session closed on an error or a lost device | the conversation at the failing event |
| repeated | the same person asked substantially the same thing again within 30 seconds | the conversation at the second ask |
| speaker flip | the next utterance in a conversation came from a different person (ADR-0016) | the conversation at the flip |

![Triage, repeated](triage-repeated.png)

A signal is derived on read, never recorded. Slow answers are not signalled
yet: the journal records speech when playback ends, not when it starts, so it
cannot yet say how long a person waited.

### Review

![Review](review.png)

One harvested barge-in at a time (`?pair=` picks one), laid out on a single
time axis: the assistant's turn with its unheard tail hatched, the mic track
with the interruption and the correction, the answering turn, and the cut.
The cut is the device's own report of the frame its DAC stopped on
(ADR-0005, ADR-0033), not the length of the text the model sent, and the
rejected clip plays only up to it. Offsets come from clip lengths and the
recorded barge-in position rather than wall clocks.

The pair flow from [Curate](#curate) sits beside the timeline, so a reviewer
can judge the pair where they heard it.

![Review, nothing to review](review-empty.png)

### Replay

![Replays](replays.png)

Edit-and-replay (SPEC §9.2, `internal/rerun`). Pick a conversation, edit the
system prompt or the model, and every recorded turn is asked again.

![Replay](replay.png)
![Replay, after a re-run](replay-rerun.png)

The result sets each turn's recorded speech and tool calls beside the
re-run's and says what changed. It is safe to point at the live model: a
turn is the system prompt plus one transcript, tool results never feed back
in, and so a re-run compares the calls the model *would* make without
executing any of them. A whole re-run is bounded at three minutes.

![Replay without a model](replay-no-model.png)

### Curate

![Curate, the mismatch guard](curate-guard.png)

The harvested candidates on the left, the pair flow on the right. A pair is
the prompt, the turn the person rejected, and a chosen answer. Accept, edit
the chosen side, or discard with a reason (*barge-in was noise*, *not a
preference*, *duplicate*, *other*); undo takes a pair back to unreviewed.

The chosen side starts as what the assistant said *after* the correction,
which answers the correction rather than the original prompt (SPEC §9.1,
ADR-0026). Accepting it unchanged is the one mistake the flow exists to
block, so the reviewer meets a guard rather than an empty editor. Accepting
anyway is allowed and marks the pair unfixed, which Export holds back.

Verdicts are rows in the curation table, revisable where the journal is
append-only; unreviewed is the absence of a row (ADR-0034).

### Export

![Export](export.png)

The dataset is every pair that is accepted, fixed, and attributed: the turn
recorded the STT, LLM and TTS versions that produced it (ADR-0032), so a
trainer can hold a configuration responsible. The page counts what each gate
holds back (unreviewed, still being edited, discarded, unfixed, unattributed)
and previews the first rows exactly as `/export/dpo.jsonl` writes them, in
the same conversational shape as `task harvest` (`harvest.Export`).

## The UI kit

`internal/reviewui/ui` is the component kit the screens are built from:
html/template partials, one stylesheet per component, and the view-model
type each partial executes against. A component is three files sharing a
name:

```
ui/<name>.go                          view model (the template's dot)
ui/templates/components/<name>.tmpl   {{define "<name>"}} partial
ui/static/css/components/<name>.css   styles, built on tokens.css
```

`ui/demo` builds the design mock's data as view models; the kit's own tests
render every component from it. `internal/reviewui/audio` frames raw journal
PCM as WAV on the way out, because a browser cannot play the device format
and the store stays raw.

## Tests

Three layers, all over the same household fixtures (`household_test.go`):
named people, real rooms, real tool calls, barge-ins cut where a DAC would
cut them.

| Layer | Where | Proves |
|---|---|---|
| handlers | `cmd/reviewui/*_test.go`, in `task test` | what the server writes, against in-memory stores |
| database | `replay_db_test.go`, `internal/curation`, in `task test:db` | the same screens over real Postgres |
| browser | `e2e_test.go`, `e2e_journeys_test.go`, in `task test:e2e` | what a reviewer gets: htmx loaded and swapping, audio a browser decodes, links that land where they say |

The browser tier drives a headless Chrome with chromedp and skips unless
`CHORUS_E2E_CHROME` names a browser; CI sets it, so there it fails rather
than skips. `CHORUS_E2E_SHOTS` keeps a full-page screenshot of every step,
and CI uploads them as the `reviewui-screens` artifact.

```sh norun
CHORUS_E2E_CHROME=$(command -v chromium) task test:e2e
CHORUS_E2E_CHROME=$(command -v chromium) CHORUS_E2E_SHOTS=/tmp/shots task test:e2e
```

`e2e_test.go` checks each screen on its own. `e2e_journeys_test.go` walks a
reviewer through a whole day, start to finish:

- triage a barge-in, open it in Review, fix the chosen side, and accept it;
- curate every pair, discarding one, then download the dataset;
- export holds back the unfixed and unattributed pairs;
- cancel and undo leave no verdict behind;
- Review walks every cut in turn;
- every Triage signal opens where it happened;
- Browse follows a person across satellites and walks back through the days.

![Journey: Browse today](e2e/journey-browse-today.png)
![Journey: editing the chosen side](e2e/journey-review-editing.png)

The screenshots in this directory come from those runs. A PR that changes a
screen replaces its screenshot here and shows it in the PR description.
