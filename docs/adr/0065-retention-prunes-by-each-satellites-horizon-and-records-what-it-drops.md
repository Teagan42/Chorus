# 0065. Retention prunes by each satellite's horizon and records what it drops

- **Status:** accepted
- **Source:** SPEC §8, §4.5, §9, §13 · ADR-0007, ADR-0021, ADR-0022, ADR-0034, ADR-0045, ADR-0052, ADR-0058, ADR-0062

SPEC §8 and CONTRIBUTING §7 promised retention configurable per satellite,
and nothing was ever deleted. Continuous capture is ~115 MB of PCM a day.
Every false wake writes audio. Nothing checked for free space before a write.
The journal is hash-partitioned by conversation (ADR-0021), so there is no
old partition to drop. The device logs and the house timer log are single
logs that grow forever, and the house log was replayed whole at every start.
Now `devices.yaml` sets the horizons, `chorusd` prunes to them, and every
clip it removes is recorded in the log that named it. This refines ADR-0007:
the log stays the runtime, but it is no longer forever.

```go
import (
	"time"

	"github.com/teagan42/chorus/internal/retention"
)

// The kitchen keeps a month of audio and a year of conversations.
var kitchen = retention.Horizons{Audio: 30 * 24 * time.Hour, Journal: 365 * 24 * time.Hour}
```

## The policy

`retention` takes `audio` and `journal` horizons in days, written `30d`. It
is set house-wide and per satellite, and a satellite's block overrides the
house's one horizon at a time. Absent or `0` keeps forever. That is the
default, so an inventory written before this changes nothing. The house
alone has `prune_curated`, which is off by default. Load refuses a horizon
that is not a number of days, and audio kept longer than the log that names
it. The error names the satellite. A satellite no longer in the inventory
falls back to the house's horizons.

## Audio

A clip belongs to the satellite whose session was open when it was recorded,
or to the device log's own satellite. The audio stream belongs to the device
(SPEC §4.5), so a conversation that walked from the kitchen to the office
ages each clip by its own room. A clip's age runs from the last event that
names it, because a barge-in names its utterance's blob before the utterance
does. Past the horizon the pruner removes the blob, then appends
`audio_dropped` (`audio_ref`, `reason: retention`, `days`) to the same log.
It writes through the daemon's own journal, whose per-log lock orders the
record with the sessions' writes. The record is a new event kind, written by
a new `store` actor. No existing kind says "this clip is gone", and an
event, once written, cannot gain a field.

Readers mark the clip as gone instead of failing on it:

- The reducer folds `audio_dropped` to no change.
- Harvest keeps each ref in place beside its text and lists the gone refs in
  `meta.audio.gone`. A wake negative says `audio_gone`.
- The review UI shows *audio pruned* where the player was. Its audio handler
  answers a missing blob with a 404 that says the audio is no longer kept.
- Browse does not count the record in a conversation's length.
- Retention itself does not count the record as activity. If it did, each
  pruned clip would put off the log's own deletion.

## Conversations

A conversation is deleted whole once its journal horizon has passed since its
last activity. The horizon is the longest of the satellites it was on, and
forever when any of them keeps forever. Its blobs go first, then its
curation rows, then its log, so a pass that fails partway finds the log again
next time. Memories a person asked to keep outlive the conversation, as
ADR-0040 decided. Summaries are already pruned after their week.

A deletion is by primary key. A whole log goes by the key's
`conversation_id` prefix, which prunes to one partition. A long-lived log's
events go by `(conversation_id, seq)`. Neither scans, so there is no
migration and no index on time. The candidates come from the listing every
reader already runs, and a log is read again only once it grew or something
in it comes due. Long-lived logs grow by the minute, but what they grow by
cannot come due sooner than their due time already says.

**A live session's log is never touched (ADR-0022).** A log is live when it
leaves a session open and did something within the last day. Sessions do not
survive a restart (SPEC §14), so a log a crash left open is not live. The
clips the disk guard declined in a live conversation wait until it ends.

## Curated

A reviewer's work is kept past every horizon unless `prune_curated` is set.
For a conversation, curated means one of these:

- a pair accepted or edited (ADR-0034);
- a turn labelled or noted (ADR-0052);
- a re-run promoted (ADR-0052, ADR-0059).

A discard is not keeping, and nor is a re-run nobody promoted. Curation is
judged for the whole conversation, since a pair is derived from its
conversation on every read (ADR-0026). In a device log it is per event: a
wake a reviewer confirmed as a hard negative keeps its event and its clip
(ADR-0058). Retention deletes curation rows only for a log, or the events
of one, that it has deleted.

## The long-lived logs

Of the two designs, bounding what a start replays is the smaller correct one.
Snapshot and truncate would add a kind every reader must fold. It would also
delete history a household that keeps everything chose to keep.

**The house timer log.** Every timer event records `replay_from`: the seq of
the earliest timer still running once the event is written, or the seq past
it when nothing is. A start reads the last event, then the log from that seq,
and folds only that. A timer started before `replay_from` had ended before
it, so no event past it names that timer. The tail therefore folds without a
contradiction, and to the same `Running()` as the whole log. The scheduler is
the house log's one writer, so it knows the next seq when it writes the field.
A log from before the field replays whole. With the house's journal horizon
set, retention deletes the timers that ended before it, each timer's events
together, so every remaining end still has its start. ADR-0007's invariant
holds both ways: the whole log still replays, and the tail rebuilds what the
daemon runs.

**The device logs.** Nothing replays these at a start: chorusd only appends
to them and reads their last seq. With the satellite's journal horizon set,
retention deletes their events older than it. A confirmed wake stays, and
so does an `audio_dropped` while the event it names stays.

**No seq is handed out twice.** A store never deletes a log's last event.
`MemStore` keeps each log's last seq apart from its length, as Postgres's
`max(seq)+1` does. A reviewer's verdict keyed by a deleted seq therefore
never lands on a new event. The review UI's per-log cache (ADR-0062) forgets
a log the listing no longer names.

## The disk guard

`blob.Guarded` wraps the blob store. Before each write it reads the free
space on the blob directory's filesystem. That reading is the `Disk`
interface, and `DirFree` is the daemon's only probe. Below the floor
(`CHORUS_BLOB_MIN_FREE_MIB`, 1024 by default, 0 for off) the writer keeps
nothing but still commits to its ref. The turn that names the ref goes on as
it would, so the household hears the same. The guard logs each crossing of
the floor once, and holds the declined ref for a day. The pruner records
`audio_dropped` with `reason: disk_low` in the log that names the ref, once
that log is not live. The guard sits where the store is composed, so the
listening child is unchanged. A disk the guard cannot read is not known to
be full, so audio is written.

## Alternatives rejected

- **Repartition the journal by time.** Old partitions could then be dropped
  whole. But every read is one conversation's log (ADR-0021), and a
  conversation may span two partitions. It is also a table rewrite of a
  household's whole history.
- **Record drops in one retention log.** Every reader of a conversation
  would need a second log to know which clips are gone. That log would grow
  forever, as the logs this ADR bounds did.
- **Treat a missing blob as pruned, recording nothing.** The log would no
  longer say what is in the corpus. A blob lost to a disk fault would read
  the same as one removed on purpose.
- **Fail the write on a low disk.** The listening child drops an utterance
  whose blob fails to write, which changes what the household hears.
- **A time window on the timer replay.** A start reading the last day only
  misses a timer that was running when the daemon went down for longer, and
  that timer is never recorded as missed.

## Forecloses

- Declined refs live in memory. A restart before the pruner journals them
  leaves a clip missing with no `audio_dropped`. Readers still answer 404
  with "no longer kept".
- A running review UI keeps serving a trimmed long-lived log until the log
  grows, because ADR-0062 keys its cache on the last seq, which trimming
  does not move. A restart, or the next event, refreshes it.
- A pruned conversation's `audio_dropped` is its latest event. The store's
  listing, ordered by latest activity, moves it up, though Browse sorts by
  start and does not show this.
- A verdict undone after its log was settled is noticed within a day for a
  conversation. For a device log it is noticed at the next start.
- Re-runs of a curated conversation are kept as long as it is, and still
  grow without bound (ADR-0059).
- The Postgres delete path is covered by db-tagged tests in
  `internal/journal`, `internal/curation` and `internal/retention`. They had
  not run where this was written, which had no Postgres.
