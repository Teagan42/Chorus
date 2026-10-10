# 0058. A rejected wake ships in its own corpus, and a repeat is evidence on its first ask

- **Status:** accepted
- **Source:** SPEC §9.1, §9.3 · ADR-0026, ADR-0034, ADR-0050, ADR-0052

SPEC §9.3 auto-labels every stage-two wake rejection as a hard negative, and
§9.1 names two more signals besides the barge-in: a completed turn nobody
corrected is a weak positive, and a repeated request is a failure. The
journal held all three, and nothing read them. Now each one is read on
request, the way pairs are (ADR-0026). A rejection is a row in a wake
corpus that is separate from the DPO dataset. A weak positive is a Triage
pile, and it is not exported. A repeat is evidence on the pair of the
answer that made the person ask again.

```go
import (
	"context"
	"io"

	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
)

// The wake corpus of one satellite, before a reviewer has said anything.
func corpus(ctx context.Context, store journal.Store, w io.Writer) error {
	ns, err := harvest.Negatives(ctx, store, "device:kitchen")
	if err != nil {
		return err
	}
	return harvest.ExportNegatives(w, ns)
}
```

**The wake corpus is its own file.** `/export/wake-negatives.jsonl` holds one
JSON object per rejection, with `label` 0, `source` `stage2_reject`, the
stage-two `reason`, the satellite, the time, and the log and seq it came
from. A negative has no chosen side, so it is not a preference pair, and
mixing it into the DPO file would hand a DPO loader rows it cannot train
on. The audio is given by blob ref, as a pair's audio is, so the clips stay
under the blob store's retention and the export copies nothing. Both
channels are kept, the processed stream and the XMOS's second output, since
SPEC §9.3 says retraining may target either (ADR-0050). `reason` is in every
row so a trainer can drop a kind of rejection without asking the UI.

**A rejection ships unless a reviewer discards it.** That is SPEC §9.3's
rule, and it does not wait on anyone, with one exception. A wake that
passed the higher threshold and the speech check, and failed only on the
voice (`unknown_speaker`), is most likely the wake word said by a guest.
Training on it as a negative would teach the model to miss the wake word.
So it is held until a reviewer confirms it. The reviewer's word is a row in
`curation_wake_verdicts`, keyed by the device log and seq: `confirmed` or
`discarded`. The last write wins, and no row means unreviewed (ADR-0034). A
confirmed row ships with `confirmed` true, so a trainer can weight what a
person heard above what only the rule passed.

**A weak positive is read, not exported.** It is a turn whose model
completed without error, and which was not cut off, asked again, or
failed. Triage gives these turns a tab of their own, kept out of *All*,
since that is the pile of problems, and Browse does not colour a block by
one. The completion is the anchor, so a weak positive never shares a seq
with a signal raised on the utterance. It is not exported: SPEC §9.1 calls
it weak, DPO needs a rejected side, and nothing in the SPEC asks for a
positives file. A reviewer who agrees labels the turn *good — exemplar*,
which keeps it a positive and makes no pair (ADR-0052).

**A repeat is evidence, not a pair of its own.** A repeat has no chosen
side: the second answer replies to the second ask, which is ADR-0026's
mistake again. So the repeat signal names the first ask it repeats, and the
answer to that ask is what the reviewer labels. A fault and a note make
ADR-0052's annotation pair, id `<conversation>/<seq>/annotation`, and that
pair carries the repeat. The second ask is added to `heard`, and its seq
and audio are the pair's `seq.correction` and `audio.correction`, the
fields a barge-in uses for what the person said instead.

## Alternatives rejected

- **The wake corpus as rows in the DPO file, flagged by source.** One
  download would be simpler, but every DPO loader would have to know to
  skip the rows, and one that does not would fail on them or, worse,
  train on them.
- **Ship every rejection with no exception.** That is the SPEC's letter.
  But `unknown_speaker` is the one gate a real wake word fails, and the
  household has guests, so shipping those rejections unread would put
  wake words into the negatives.
- **Hold every rejection for a confirm.** That is safe, but it undoes what
  SPEC §9.3 is for: the corpus fills itself without a reviewer.
- **Embed the audio in the export.** It would make the file
  self-contained, but it would copy every clip out from under the blob
  store's retention, and the DPO rows do not do that either.
- **Export weak positives as an SFT file.** Nothing asks for one, and an
  unread "nobody complained" is the weakest label the log holds.

## Forecloses

- A wake corpus row is keyed by its device log and seq. Moving rejections
  out of `device:` logs, or renumbering one, would orphan the verdicts on
  them.
- The `label`, `source` and `reason` vocabulary is now a format trainers
  read. A new stage-two gate adds a `reason` value, and changing an
  existing value breaks their filters.
