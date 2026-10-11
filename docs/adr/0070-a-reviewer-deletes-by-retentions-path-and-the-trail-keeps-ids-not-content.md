# 0070. A reviewer deletes by retention's path, and the trail keeps ids, not content

- **Status:** accepted
- **Source:** SPEC §5, §8 · ADR-0022, ADR-0034, ADR-0040, ADR-0052, ADR-0058, ADR-0059, ADR-0062, ADR-0065

The review UI could not delete anything. "Delete that recording of my
guest" and "forget what it remembered about Alice" are basic requests for
something that records a kitchen (CONTRIBUTING §7), and the only deletion
was retention's, on a timer (ADR-0065). Now a reviewer can delete a
conversation, drop one event's audio, and forget any memory or summary.
Each deletion leaves a line on a Deletions screen, and a `-read-only`
review UI does none of it.

```go
import "github.com/teagan42/chorus/internal/retention"

// What the review UI deletes through, and the refusals it says on the page.
var (
	_ = (*retention.Eraser).DeleteConversation
	_ = (*retention.Eraser).DropAudio
	_ = []error{retention.ErrLive, retention.ErrResumable, retention.ErrLongLived}
)
```

## One deletion path

`retention.Eraser` is the code a pass already ran, exported. The pruner now
deletes through it, and the review UI builds one over its own stores.

- **A conversation** goes in retention's order: every clip it names, then
  its curation rows, then the summaries kept of it, then its log. A
  deletion that fails partway is found again by its log, as a pass is.
  The summaries go because they are the conversation's words kept for the
  people in it (ADR-0043). Facts someone asked to keep stay, as ADR-0040
  and ADR-0065 decided; the memory screen marks where they came from as
  *no longer in the journal*. The review UI forgets what it derived of the
  log at once, so no screen serves it from the cache ADR-0062 keeps.
- **One event's audio** goes clip by clip, the event's own and its second
  channel's. Each removal is recorded as `audio_dropped` with a new reason,
  `redacted`, in the log that named it. Every reader already shows a
  dropped clip as gone (ADR-0065), so the page shows *audio redacted*
  where the players were, and the export lists it as gone. The words stay:
  an event, once written, cannot change (SPEC §8). To remove the words too,
  a reviewer deletes the conversation.
- **A fact** is forgotten through `memory.Store.Forget`, the call the
  `forget` tool makes. It is keyed by its person, so a reviewer cannot
  forget Teagan's fact by naming it under Alice. **A summary** is forgotten
  through a new `memory.Kept`, which also lists every fact and summary,
  private ones included. The review UI already plays every recording in
  the house; a household that exposes it has accepted that (SPEC §1).

## What it refuses

A deletion is refused, with a sentence in the alert region, for:

- **a live session's log**, by retention's own definition: one that leaves
  a session open and did something within the day (ADR-0022, ADR-0065);
- **a conversation its person may still resume.** One that did something
  within `session.MigrationWindow` (two minutes) may be opened again by
  chorusd when its person wakes another satellite (SPEC §4.5). Retention
  never meets this case, since its horizons are days;
- **a satellite's or the house's own log.** chorusd writes these all day,
  and it writes through its own journal, whose per-log lock orders its
  writes. The review UI is another process. An `audio_dropped` from it
  could take the seq chorusd was about to write, and Postgres would refuse
  one of the two. Retention trims these logs instead;
- **a post without its second press**, or from read-only mode, another
  site's page or a name the UI was not told is its.

Every destructive route is a POST behind the cross-origin check and the
Host allow-list the verdicts sit behind. The second press is a `<details>`
panel, the kit's `confirm` component. It says what goes and that nothing
keeps a copy, and its button posts a hidden `confirm` field the server
checks. The panel needs no script, so the policy's ban on inline script
holds, and the form still posts without htmx.

## The trail

Each deletion writes one row to `erasures`, a table beside the journal in
its own package, `internal/erasure`. A row holds:

- the kind: conversation, audio, memory or summary;
- the conversation it was, or was in, or came from;
- the clips' refs, or the memory's id;
- whose memory or summary it was;
- a count of what went, such as "8 events, 5 clips";
- when.

It never holds a transcript, a fact or a summary. A trail that kept the
content would undo the deletion it records.

Not in the journal: the conversation's own log is gone. An event appended to
it would bring the log back, holding exactly the record of its deletion. A
house-wide log of deletions is the retention log ADR-0065 rejected, one more
log every reader must skip. A forgotten memory was never in the journal at
all.

Not in the curation tables either, though they too sit beside the journal
(ADR-0034). `curation.Forget` deletes a conversation's rows as part of
deleting it, so the trail would be in the path of the deletion it records.
A verdict is also a revisable judgment, and a trail is the opposite: rows
are only added. `erasure.Store` has no delete.

The deletion happens first, then its row. If the row cannot be written, the
page says the deletion happened and was not recorded. The other order would
leave a line for a deletion that failed.

## Read-only

`-read-only`, or `CHORUS_REVIEWUI_READ_ONLY=true`, hides every delete, drop
and forget control and refuses their routes with a 403 that names the flag.
A value that is not a boolean stops the UI from starting. Verdicts and
labels are not deletions and still work; a reviewer who may label but
should not delete is the case this is for.

## Alternatives rejected

- **The browser's `confirm()`.** It is inline script, which the policy
  refuses, and a dialog the browser may suppress.
- **A typed confirmation.** Conversation ids are long, and typing one proves
  attention less than reading a panel that says what will go.
- **A soft delete.** A tombstone that hides a conversation keeps the
  recording of the guest, which is the thing asked to go.
- **Deleting in chorusd on the review UI's request.** It would let the UI
  drop a clip from a satellite's own log, such as a guest's rejected wake.
  It also needs a channel between two processes that share only a
  database. Left for when that case is asked for.

## Forecloses

- A clip in a device log, such as an `unknown_speaker` wake that was a
  guest's voice, cannot be dropped from the review UI. Retention removes
  it by its horizon.
- The trail says what went and when, not who: there is no login (SPEC §1).
  Retention's own deletions are not on it.
- A forgotten fact is still in the `remember` call in its conversation's
  log until that conversation is deleted (ADR-0040). The panel says so.
- A conversation id names its room and minute, and a ref names its clip.
  The trail keeps both, which says that something happened in the living
  room at 20:15, never what.
- The conversation ids a reviewer deleted are never handed out again, as
  no log's id is (ADR-0065).
- The Postgres paths are covered by db-tagged tests in `internal/erasure`,
  `internal/memory` and `internal/retention`. They had not run where this
  was written, which had no Postgres.
