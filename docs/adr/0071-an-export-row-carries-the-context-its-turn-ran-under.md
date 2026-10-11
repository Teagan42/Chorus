# 0071. An export row carries the context its turn ran under

- **Status:** accepted · refines [ADR-0026](0026-harvest-barge-ins-as-uncurated-pairs.md), [ADR-0052](0052-labels-and-promoted-re-runs-live-beside-the-journal-and-make-pairs-on-read.md) and [ADR-0058](0058-a-rejected-wake-ships-in-its-own-corpus-and-a-repeat-is-evidence-on-its-first-ask.md)
- **Source:** SPEC §8, §9.1, §9.2, §13 · ADR-0032, ADR-0034, ADR-0054, ADR-0059, ADR-0065

The dataset export was not usable away from the review box. A row carried
the user turn and the version labels of what produced it, but no system
prompt and no tool declarations. A promoted re-run's edited prompt and
schema sat in `curation_promotions` and never shipped. ADR-0026 said a
training run would prepend "the versioned artifact" (SPEC §13), but the
labels are content hashes (`ollama.PromptVersion`, `ToolSchemaVersion`;
SPEC §8). A build can name only its own prompt, and nothing kept the text
of the others. There was also no format version, no dedupe, no split, no
`decided_at`, no file for *good — exemplar* turns, and audio was blob refs
that stop resolving off the box or after retention (ADR-0065).

The export is now format 2. Every row carries its context, and the corpus
ships as JSONL or as one zip with its clips.

```go
import (
	"io"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/harvest"
)

// The curated rows, each with the prompt and tools it ran under, a tenth
// of conversations held out.
func dataset(w io.Writer, pairs []harvest.Pair, good []harvest.Exemplar) error {
	d, err := harvest.Build(pairs, good, harvest.Options{Contexts: contexts.Default(), Validation: 0.1})
	if err != nil {
		return err
	}
	return d.WriteDPO(w, harvest.SplitTrain)
}
```

## Where a version's text comes from

`internal/contexts` archives the text behind every label. Each prompt
version is a JSON file holding the persona, plus the summary prompt when
the label hashed it too. Each tool-schema version is the declarations as
`/api/chat` was sent them, indented. A file is named by its label, and a
test recomputes every hash. So an entry cannot hold text other than what
its label names, and a hand edit fails `task test`. Another test fails
until the build's own prompt and schema are archived.
`CHORUS_UPDATE_CONTEXTS=1 go test ./internal/contexts` files them. A
change to `DefaultPrompt` or to the tool registry therefore ships with its
archive entry. The archive was seeded from history: every commit on main
that changed the prompt, the summary prompt, the wire shape or the
registry was built, and its labels and text were taken from that build.
That gave 4 prompt versions and 8 tool-schema versions.

`contexts.Default()` asks the running build first, then the archive. A dev
build with an unarchived prompt still resolves its own label. A promoted
re-run's row takes the prompt and schema its promotion kept. A promotion
from before ADR-0059 kept no schema, so its label is resolved instead.

**A label nothing holds is never guessed.** The row ships. It names the
missing half in `meta.context_missing` (`prompt`, `tool_schema`, or both)
and leaves out the system message or `tools` it does not have. A
neighbouring version's text would be a context the model never saw, which
is the mistake ADR-0026 kept unheard speech out of prompts to avoid.
Labels from builds off main, such as a dev branch someone deployed, are
the expected case.

The journal is unchanged. It already records the label of every
completion, and a label is enough once its text is kept.

## The row

Each row is self-contained. `schema_version` is 2. `prompt` opens with a
`system` message holding the persona, and `tools` is the array of
declarations. This is the shape TRL's conversational DPO and SFT loaders
read without a join. A replay row's context is what its chosen side ran
under: the reviewer's edit is what made that side right. The rejected
side's versions stay in `meta.versions`.

The system message is the versioned persona, not the whole message the
engine sent. The engine appends the speaker, the room, the time, the
memories and the recent conversations. The row carries all of these in
meta except the room, which the harvested turn does not hold.

`meta` adds provenance:

- `verdict` and `decided_at`, from the reviewer's stored decision;
- `context` (the labels the text is for) and `context_missing`;
- `split`;
- `duplicates`.

Source, conversation, seq, model, and STT and TTS identities were there
already. Who decided is not recorded: the review UI has no accounts, and
ADR-0034 keeps one current verdict without a history. So no row names a
reviewer.

## Dedupe

A turn can make a barge-in pair, a note's pair and a replay's pair. Two
rows are the same preference when they share all of these:

- conversation and turn;
- context labels;
- rejected side, speech and calls;
- chosen side, speech and calls.

Whitespace in the speech is collapsed. One row is kept: the barge-in first,
then the note, then the replay. It lists the others in `meta.duplicates`
and takes their labels. Rows that differ in either side, or in context,
are different preferences, and both ship.

## Split

A row is in validation when the first eight bytes of the SHA-256 of its
conversation id, read as a fraction of 2^64, fall below the validation
fraction. So the split depends on the conversation id alone. A
conversation never straddles it, the same export splits the same way
every time, and raising the fraction only moves conversations into
validation. The fraction is `?validation=` on every download, 0.1 by
default. `?split=` downloads one side. Without it, every row says its
side in `meta.split`.

## SFT

A turn labelled *good — exemplar*, with no fault label beside it, is one
row of `/export/sft.jsonl`. Its `completion` is the turn as generated,
with the calls it made beside its speech. Unattributed or silent turns are
held, and the page counts them. Weak positives stay out, as ADR-0058
decided: nobody read them, and "nobody complained" is the weakest label
the log holds. An exemplar is a person's judgement.

## The archive

`/export/dataset.zip` holds these files:

- `dpo/` and `sft/`, each split into `train.jsonl` and `validation.jsonl`;
- every clip a row names, at `audio/<blob key>.wav`, framed as a 16 kHz
  mono WAV;
- `manifest.json`, with the schema version, the time, the fraction, the
  split and dedupe rules, row counts and the gone clips.

A row's `meta.audio.paths` maps each ref to its file. Some clips are not
copied: one the log says retention dropped, and one the store no longer
has. Each is listed in `meta.audio.gone` and has no path. A store failure
other than a missing clip aborts the download, rather than handing over a
short corpus that looks whole.

## Alternatives rejected

- **Journal the prompt text on each event.** It would grow every event by
  kilobytes to say what a 19-character label already names. The text is
  still needed for every event already written.
- **A contexts table rows refer to.** It keeps rows small, but every
  loader would need a join before training. Inside the zip, deflate already
  reduces a repeated context to almost nothing.
- **Regenerate old versions from git at export time.** A review box has no
  repository and no toolchain. The text has to be in the binary.
- **Ship weak positives as SFT.** See ADR-0058.
- **A reviewer field from a flag.** It would stamp the person exporting
  onto decisions someone else may have made.

## Forecloses

- The archive only grows. Each prompt or registry change adds a file of a
  few kilobytes, and an entry is never rewritten, since its label is its
  text's hash.
- A label recorded by a build whose text was never archived stays
  `context_missing` for good.
- Format 1 rows carry no `schema_version`. A loader that reads both must
  treat its absence as 1.
- The wake corpus is not in the zip. It is still its own JSONL of blob
  refs (ADR-0058).
- An archive is built in one pass and holds one clip in memory at a time,
  up to the 32 MiB the audio handler also caps a blob at.
