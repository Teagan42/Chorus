# 0076. A reviewer drops a clip from a satellite's own log, and a write that loses its seq goes after

- **Status:** accepted · refines [ADR-0070](0070-a-reviewer-deletes-by-retentions-path-and-the-trail-keeps-ids-not-content.md)
- **Source:** SPEC §8, §9.3, §14 · ADR-0007, ADR-0021, ADR-0058, ADR-0065, ADR-0070, ADR-0072

ADR-0070 let a reviewer drop a clip from a conversation but not from a
satellite's own log, `device:<satellite>`. That log holds the wakes stage
two rejected, and an `unknown_speaker` one is often a guest saying "Hey
Eddie" (ADR-0058): exactly the recording a household asks to go. Retention
removed it only by its horizon. Now a reviewer drops it from the review UI,
by the same path, panel and record as a conversation's clip.

```go
import (
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/retention"
)

// What the review UI asks before it offers a drop, and what a writer that
// lost its seq to another process is told.
var (
	_ = (*retention.Eraser).RefusesDrop
	_ = journal.ErrSeqTaken
)
```

## Why it was refused, and what changed

The refusal was about the seq, not the audio. chorusd writes a device log
all day, through its own journal, whose per-log lock orders its writers.
The review UI is another process. Its `audio_dropped` could land between
chorusd's `LastSeq` and its `Append`; the store refuses the second writer
to claim a seq (SPEC §8, ADR-0021), so chorusd would have lost the event it
was writing. A lost `wake_rejected` is a lost hard negative, and its clip,
already written, is named by no log, so retention never removes it.

**The journal now writes after the other writer instead of losing.** Both
stores wrap that refusal as `journal.ErrSeqTaken`, and `Journal.Append`
reads `LastSeq` again and writes at the next seq, up to five times. Within
one process the lock means this never happens. Across processes the event
lands after the other one, the log stays gapless, and nothing is lost. The
store conformance suite already described a caller retrying at the new
`LastSeq` as how a real caller resolves the loss; `Append` is now that
caller.

**A satellite's log takes a drop at once.** `Eraser.RefusesDrop` says what a
drop would refuse. For a conversation it is `Refuses`, as before: not while
a session is live or its person may resume it. A device log has no session,
so it has no window to wait out. The house log is still refused: it names no
audio. Deleting any long-lived log whole is still refused, as ADR-0070 and
ADR-0065 decided; retention trims those.

What a drop does is ADR-0070's, unchanged. Each clip the event names goes,
its second channel too. Each is recorded as `audio_dropped` `redacted` in
the log that named it, and Deletions keeps a line. The wake's record stays,
with any verdict on it. The wake corpus lists the row as `audio_gone`,
as it lists a pruned one.

## Alternatives rejected

- **A side table of dropped refs, read by every reader.** It is a second
  record of what audio is gone, beside `audio_dropped`, and every reader of
  the log would need it: the review UI, the harvester, the exports and
  retention. One record is the point of ADR-0065.
- **Ask chorusd to drop it.** ADR-0070 rejected this as needing a channel
  between two processes that share only a database. A request table chorusd
  polls is such a channel, and it delays the drop to the next poll.
- **A Postgres advisory lock both processes take.** It orders the writes,
  but costs a round trip on every append chorusd makes, for a write the
  review UI makes a few times a year.

## Forecloses

- A device log's event can take a seq a moment later than chorusd read.
  Its wall clock is taken again at the retry, so seq order and time order
  still agree.
- A writer that loses five times running fails as before. That takes five
  other processes writing one log in the same instant, which nothing does.
- The Postgres paths are covered by the db-tagged store conformance suite,
  which had not run where this was written, which had no Postgres.
