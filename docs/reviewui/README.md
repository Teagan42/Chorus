# Review UI

`cmd/reviewui` is where a person reads what the household's satellites did and
turns the barge-ins worth learning from into a preference dataset (SPEC §9).
It is Go and htmx over the same Postgres journal and blob directory `chorusd`
writes, and it is a reader: every row on every screen is derived from the log
on request, never copied out of it (SPEC §8, ADR-0007). What it writes is
a reviewer's word: a verdict on a pair, a turn's labels, a promoted re-run,
and whether a rejected wake ships as a hard negative. Those live in their
own tables beside the journal, not in it (ADR-0034, ADR-0052, ADR-0058).

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

Without a checkout, each [release](https://github.com/Teagan42/Chorus/releases)
attaches `reviewui_<version>_<os>_<arch>.tar.gz` beside `chorusd`'s. The binary
carries its templates and htmx, so it needs only the variables below.

| Variable | Required | What for |
|---|---|---|
| `CHORUS_POSTGRES_DSN` | no | The journal. Defaults to the docker-compose database. |
| `CHORUS_BLOB_DIR` | yes | The audio the journal refers to; served to the browser as WAV. |
| `OLLAMA_URL`, `OLLAMA_MODEL` | no | What Replay re-runs turns against. Unset, Replay shows the recorded turns and says it has nothing to ask. |

These are the same variables `chorusd` reads, so the `.env` copied from
`.env.example` already serves both. `/` opens [Browse](#browse).

There is no login: like `chorusd`, the review UI trusts the household's
network (SPEC §1). What it refuses is a write another site's page sends from
the reviewer's browser. A POST whose `Sec-Fetch-Site` is anything but
`same-origin`, or, from a browser too old to send that, whose `Origin` names
another host, is a 403 (Go's `http.CrossOriginProtection`). A request with
neither header, such as `curl`, is not a browser's and lands. A proxy in front
of the UI must pass the `Host` header through.

## The hosted demo

[teagan42.github.io/Chorus/demo](https://teagan42.github.io/Chorus/demo/)
is this UI running in a visitor's browser over one made-up household's
Thursday: Teagan, Alice and Alan, the kitchen, office and living room, three
barge-ins, three music searches that each say a few words while they look,
a garage sensor that times out, an oven timer asked for twice, a pasta timer
called off, the television the barge-in gate refused, a front
door that kept Teagan waiting, and Alice carrying her music to the living
room. It is the same day the browser
tests walk (`internal/reviewui/household`), so what the demo shows is what CI
checked.

There is no server behind it. `cmd/reviewui` compiles to WebAssembly
(`main_js.go`) and serves the household from memory inside the tab;
`cmd/reviewui/web` is the shell page that hands it every request the screens
make and routes the address bar's `#/path` to it, so a link or a reload lands
where it says.

![The demo, open on the household's Thursday](demo-browse.png)

Three things differ from a household's own review box, and a notice above
every screen says so:

- verdicts and kept re-runs live in the tab and are gone on reload;
- Replay asks `household.Model`, not Ollama: under the default prompt and
  the tools `chorusd` offers, `tools@8`, it answers each turn as the journal
  recorded it under `tools@7`, except that with no `media_search` to call
  Alice's album, Alan's jazz and his ask for something quieter are each
  answered "I can't search for music yet", with nothing played;
  under an edited prompt or tool schema it answers as the model did once
  told to lead with the count, offer the first, and stop, never calling a
  tool it was not offered;
- the clock is fixed at 22:30 that Thursday, when the reviewer sits down.

![The demo's Replay, Teagan's forecast under a prompt edited to be brief](demo-replay.png)

The audio is synthetic. `voice.json` scripts who says what and for how long,
and `task household:voice` speaks it with Kokoro, the TTS `chorusd` uses, into
16 kHz device PCM. A cut answer is spoken in two halves so the cut lands on
the word the journal recorded. `task household:voice:clone` gives the people
you name a voice cloned with Chatterbox from a reference recording of them,
fitted to the same lengths; it runs on your machine, and the clips it writes
are committed and public, so clone only voices whose owners said yes.

```sh norun
task demo:serve        # build into site/demo, serve http://127.0.0.1:8090/demo/
task household:voice   # re-speak the clips after editing voice.json
task household:voice -- --who alice --who assistant   # only theirs
task household:voice:clone -- --voice teagan=teagan.wav --voice alan=alan.wav
task household:voice:test   # the generators' own tests, with stand-in engines
```

The docs workflow builds the demo on every PR and publishes it with the site.
`demo_e2e_test.go` serves the built demo under the Pages path in a headless
Chrome, so a URL the shell fails to re-point is a 404 the test fails on.

## The screens

The header runs left to right in the order a reviewer usually works, and
badges the number of harvested pairs nobody has judged yet.

Every screen but a conversation's own reads the whole household. Each
request asks the journal how far every log has got and reads again only
the logs that grew since the last one: the journal is append-only, so what
a log derives (its pairs, signals, summary and turns) is kept by the seq it
was read to, and an append is on the next request (ADR-0062). A log no
reducer can read, such as a recall written cut off mid-memory, is left out
rather than failing the screen: Browse, Triage, Review, Replay, Curate and
Export each name the logs they skipped above what they could read, and the
server log says why.

![Browse, with one log it could not read](browse-skipped-log.png)

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
slow answer, a speaker flip. Ticks mark wakes rejected at stage two (SPEC §9.3), which
opened no conversation and so live in the satellite's own `device:` log.
Each tick is a link to that rejection in the log, and a lane's name opens
the whole log. The
shaded band behind a lane is when its mmWave radar saw someone in the room,
from the same log (ADR-0050). A gap inside an evening is the native API
dropping, not the room emptying. A satellite with no radar, such as a Voice
PE, has no band. Ticks and bands carry hidden text with their times, and a
block says what flagged it, so the day reads to a screen reader too.

![Conversation](conversation.png)

`/conversations/{id}` is one conversation's journal, event by event, with the
audio each event refers to, each player named for the event and whose it is.
Every row carries a `#seq-N` anchor, which is how
Triage links straight to the event that raised a signal. Each answer's first
audio is its own row, saying how long the person waited.

![Conversation, first audio](conversation-first-audio.png)

A model that was down, broke or went quiet past its deadline is a `model
failed` row, and a voice that failed mid-answer is a `voice failed` row.
Each says what the provider reported and which canned line apologised for
it. A truncation names what cut it, so the forecast Kokoro dropped reads as
the voice failing, not as someone talking over it (ADR-0051).

![Conversation, the model and the voice failing](conversation-provider-failure.png)

A barge-in the gate refused is a row of its own, playing what it heard,
such as the living room's television talking over an answer and turned
away at speaker ID. It is no turn and offers no labels: it is kept to tune
the gate on (SPEC §4.3).

A rejected wake's row plays what the wake model heard and, when the
satellite kept one, its second channel, captioned as such. Under it sit
*Confirm negative* and *Discard*, the reviewer's word on whether the clip
ships in the wake corpus [Export](#export) offers. A rejection ships unless
it is discarded, except one only the voice check failed (`unknown_speaker`),
which may be the wake word from a guest and so waits for a confirm
(ADR-0058). Pressing the same verdict again takes it back.

![A rejected wake, confirmed](e2e/journey-wake-confirmed.png)

The log also shows what the model was told and what the house vouched for.
A `memory_recalled` row lists each memory the turn was told, whose it is
when someone shared it, and the earlier conversations it was told of. A
held call's `confirmation_requested` row names the call and its nonce, and
`confirmation_given` says which utterance the nonce was redeemed after: the
"did they actually say yes" audit (SPEC §6, ADR-0038). The summary written
when the conversation closed is its last row. Browse measures a
conversation to its close, not to the summary.

![The door waits for a yes: recall, nonce and summary](e2e/journey-door-audit.png)

Under each utterance sit SPEC §9.2's labels for the turn it opens:
*transcript wrong*, *misunderstood intent*, *wrong tool / args*, *should
have spoken*, *spoke when it shouldn't*, *too slow*, *wrong person*, *good —
exemplar*, and a free-text "what it should have done". A turn with any fault
and a note becomes a pair in [Curate](#curate), the recorded take rejected
and the note chosen, so write the note as the reply you wanted. An exemplar
is a positive, not a pair (ADR-0052).

![Labelling a turn](e2e/journey-labels.png)

A request the person had to repeat is a turn worth labelling. The repeat's
row offers *Label the first answer*, which jumps to the labels of the turn
that failed, and those labels say when it was asked again and what was
said. Its pair carries the repeat as evidence: the second ask is in the
pair's `heard`, and its seq and audio are the pair's correction (ADR-0058).

![The oven ask Alan repeated, labelled](e2e/journey-repeat-labelled.png)

A timer going off, or someone asking for something to be said in another
room, opens a session with no wake word (ADR-0045). Browse lists it as an
*announcement*, named by what it said and whom it was for. Its log says why
it was said. Timers live in the household's own log, `house:timers`, which
Browse does not list. Its events appear in the conversations they name, as
`house #N` rows: the timer being set beside the `timer_start` call, its
cancellation beside the `timer_cancel` call, and its going off in the
session that said it. A timer nobody heard is a failure in
Triage, which opens the house log at the event.

![An announcement: the oven timer going off](e2e/journey-oven-goes-off.png)

### Triage

![Triage](triage.png)

Every conversation in the household scanned for five signals of trouble
and one of things going right (`internal/triage`), newest first,
filterable by tab:

| Signal | Raised when | Opens |
|---|---|---|
| barge-in | someone interrupted the assistant mid-turn | the pair in Review |
| failure | a tool errored or timed out (ADR-0008), the model finished with an error, or the session closed on an error or a lost device | the conversation at the failing event |
| repeated | the same person asked substantially the same thing again within 30 seconds | the conversation at the second ask |
| slow | the first audio of an answer came more than 700 ms after the person stopped speaking (SPEC §11, ADR-0035) | the conversation at the answer's first audio |
| speaker flip | the next utterance in a conversation came from a different person (ADR-0016) | the conversation at the flip |
| weak positive | a turn's model completed without error, and the turn was not cut off, asked again, or failed (SPEC §9.1) | the conversation at the completion |

![Triage, repeated](triage-repeated.png)
![Triage, slow](triage-slow.png)

Weak positives have a tab of their own and are left out of *All*, which is
the pile of problems; Browse does not colour a block by one. They are not
exported: a DPO pair needs a rejected side, and SPEC §9.1 calls them weak.
A reviewer who agrees labels the turn *good — exemplar*, which keeps it a
positive and makes no pair (ADR-0058).

![Triage, weak positives](triage-weak-positives.png)
![Triage, no weak positives](triage-weak-positives-empty.png)

A signal is derived on read, never recorded. The slow signal reads
`speech_started`, which the Speaking child journals the moment the device
reports the turn's first frame played, with the wait since the person
stopped speaking as `wait_ms`.

### Review

![Review](review.png)

One harvested barge-in at a time (`?pair=` picks one; a reviewer's own pairs have no cut to hear, so Review skips them), laid out on a single
time axis: the assistant's turn with its unheard tail hatched, the mic track
with the interruption and the correction, the answering turn, and the cut.
The cut is the device's own report of the frame its DAC stopped on
(ADR-0005, ADR-0033), not the length of the text the model sent, and the
rejected clip plays only up to it. Offsets come from clip lengths and the
recorded barge-in position rather than wall clocks.

The pair flow from [Curate](#curate) sits beside the timeline, so a reviewer
can judge the pair where they heard it. What the cut turn was told it
remembers is listed under the inspector: a cut is judged against what the
model knew.

![Review, nothing to review](review-empty.png)

### Replay

![Replays](replays.png)

Edit-and-replay (SPEC §9.2, `internal/rerun`). Pick a conversation, edit the
system prompt, the model or the tool schema, and every recorded turn is
asked again.

![Replay](replay.png)
![Replay, after a re-run](replay-rerun.png)

The result sets each turn's recorded speech and tool calls beside the
re-run's and says what changed. It is safe to point at the live model: a
turn is the system prompt plus one transcript, tool results never feed back
in, and so a re-run compares the calls the model *would* make without
executing any of them. A whole re-run is bounded at three minutes. Each
turn also lists what it was told it remembers, which the re-run is told too.

The tool schema is the declarations the model is offered, in the shape
`/api/chat` sends them, starting from what `chorusd` offers: every tool in
the registry but the deferred, so no `media_search` while media is deferred
(ADR-0060). Drop a tool, reword a description, change what a parameter
takes. The edit is read back on the server before anything is asked, and
one that does not read is refused on the page with the line it broke on.
It is diffed against what `chorusd` offers, trimmed to the lines near each
change, and the re-run's tool-schema version is the server's, a hash of
what the model was sent, so an unedited schema runs under `chorusd`'s
version (ADR-0059). That is the recorded one unless the turn was recorded
under an older schema; then the page says so beside the editor, and a turn
that called a tool no longer offered is asked without it.

![Replay, a tool's description reworded](e2e/journey-replay-tool-schema.png)
![Replay, a tool schema that does not read](e2e/journey-replay-tool-schema-malformed.png)

Every re-run that reaches a turn is kept beside the journal, promoted or
not: what it ran under and what each turn did. Replay lists a
conversation's kept re-runs newest first, and each opens at
`/replays/{id}/runs/{run}` with its comparison and the editor holding the
model, prompt and tools it ran under (ADR-0059).

![Replay, two kept re-runs](e2e/journey-replay-kept-runs.png)

A turn whose re-run changed can be promoted, from a run just made or any
kept one, with or without a model configured. The kept take becomes the
chosen side of a pair whose rejected side is what the turn recorded on its
first ask, the take Replay set beside it, and it lands in Curate accepted,
since promoting is the verdict. The take, the calls it would make, the
versions it ran under and the edited prompt and tool declarations are
stored, because the journal never held them (ADR-0052); the page sends
only which run and turn. A take that only calls can be promoted too: the
garage re-run that checks the contact sensor and says nothing until it
answers is a chosen side (ADR-0054).

![Replay, a re-run promoted](e2e/journey-replay-promoted.png)

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

Pairs come from three places, named on each row: `barge-in`, harvested from
the log; `annotation`, a turn labelled with a fault and what it should have
done; and `replay`, a promoted re-run. A reviewer's pair shows what made it
under its chosen side. Changing an annotation's note, or its *wrong tool /
args* label, asks for a new verdict, since the old one judged a different
pair. Each side lists
the tool calls it carries under its speech, exactly as the export writes
them, and a take that only calls is named by its calls in the list
(ADR-0054).

![Curate, a labelled turn's pair](e2e/journey-curate-annotation.png)
![Curate, a re-run that only calls the contact sensor](e2e/journey-curate-calls-only.png)
![Curate, a labelled turn with its repeat as evidence](e2e/journey-repeat-curate.png)

Verdicts are rows in the curation table, revisable where the journal is
append-only; unreviewed is the absence of a row (ADR-0034). Labels, kept
re-runs and promoted takes are rows beside them (ADR-0052, ADR-0059).

### Export

![Export](export.png)

The dataset is every pair that is accepted, fixed, and attributed: the turn
recorded a completion stamped with the model, prompt and tool-schema versions
that produced it, so a trainer can hold a configuration responsible. The STT
and TTS identities ride along in `meta.versions` but are not checked: a pair
without them still exports (ADR-0032). The page counts what each gate
holds back (unreviewed, still being edited, discarded, unfixed, unattributed)
and previews the first rows exactly as `/export/dpo.jsonl` writes them
(`harvest.Export`). This is the only curated export: `task harvest` writes
the same shape but raw, every candidate with `meta.curated=false` and no
chosen side, which a DPO loader cannot train on. An annotation's row carries
its labels in `meta.labels`; a replay's carries the re-run's configuration
and calls in `meta.chosen_versions` and `meta.chosen_calls`, and is
attributed only when both sides are. Each side's message carries its
`tool_calls` beside its content: a correction or a note keeps the turn's
calls on both sides, a replay's chosen side calls what the re-run would, and
a *wrong tool / args* note carries none and says `meta.speech_only`
(ADR-0054).

A row that cannot be written partway through the download aborts it, so the
browser reports a failed download rather than saving a short file that looks
whole. The server log names the row.

Below the dataset is the wake corpus: every wake a satellite rejected at
stage two, auto-labelled a hard negative (SPEC §9.3), as its own file at
`/export/wake-negatives.jsonl` (`harvest.ExportNegatives`). It is not a
preference pair, so it never enters the DPO file. The page counts what
ships, how many a reviewer confirmed, how many are held as
`unknown_speaker` waiting for a confirm, and how many were discarded. Each
row is one JSON object:

```json
{"id":"device:kitchen/7","label":0,"source":"stage2_reject","reason":"no_speech","satellite":"kitchen","at":"2025-10-09T14:02:00Z","audio":"blob://wake/kitchen-dishwasher","second_audio":"blob://wake/kitchen-dishwasher-second","confirmed":true,"conversation_id":"device:kitchen","seq":7}
```

`audio` and `second_audio` are blob refs, as the DPO rows' audio is, so the
clips stay under the blob store's retention. `second_audio` is empty
when the satellite kept no second channel. `reason` is what stage two said,
so a trainer can keep or drop each kind (ADR-0058).

![Export, the wake corpus](e2e/journey-wake-export.png)

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
and the store stays raw. Its `from` and `to` bound the clip in device frames;
one past the longest blob it serves is a 400.

## Tests

Three layers, all over the same household fixtures (`internal/reviewui/household`, the day the demo serves):
named people, real rooms, real tool calls, barge-ins cut where a DAC would
cut them.

| Layer | Where | Proves |
|---|---|---|
| handlers | `cmd/reviewui/*_test.go`, in `task test` | what the server writes, against in-memory stores |
| database | `replay_db_test.go`, `internal/curation`, in `task test:db` | the same screens over real Postgres |
| browser | `e2e_test.go`, `e2e_journeys_test.go`, `e2e_signals_test.go`, in `task test:e2e` | what a reviewer gets: htmx loaded and swapping, audio a browser decodes, links that land where they say |

The browser tier drives a headless Chrome with chromedp and skips unless
`CHORUS_E2E_CHROME` names a browser; CI sets it, so there it fails rather
than skips. `CHORUS_E2E_SHOTS` keeps a full-page screenshot of every step,
and CI uploads them as the `reviewui-screens` artifact.

```sh norun
CHORUS_E2E_CHROME=$(command -v chromium) task test:e2e
CHORUS_E2E_CHROME=$(command -v chromium) CHORUS_E2E_SHOTS=/tmp/shots task test:e2e
```

`e2e_test.go` checks each screen on its own. `e2e_journeys_test.go` and
`e2e_signals_test.go` walk a reviewer through a whole day, start to finish:

- triage a barge-in, open it in Review, fix the chosen side, and accept it;
- curate every pair, discarding one, then download the dataset;
- export holds back the unfixed and unattributed pairs;
- cancel and undo leave no verdict behind;
- Review walks every cut in turn;
- every Triage signal opens where it happened;
- Browse follows a person across satellites and walks back through the days;
- the oven timer goes off in an empty kitchen, and is shown set where it was;
- the front door waits for Teagan's yes, and the log shows the whole audit;
- a turn is labelled, its note becomes a pair, and the pair ships with its labels;
- a re-run is promoted, lands accepted, and ships saying which prompt wrote it.
- a rejected wake is opened from its tick, heard on both channels, confirmed, and shipped in the wake corpus;
- the answer Alan had to repeat is labelled from the repeat, and ships with the repeat as evidence;
- a weak positive is found in its own pile and labelled an exemplar, making no pair.

![Journey: Browse today](e2e/journey-browse-today.png)
![Journey: editing the chosen side](e2e/journey-review-editing.png)

The screenshots in this directory come from those runs. A PR that changes a
screen replaces its screenshot here and shows it in the PR description.
