# 0022. Hand the conversation over on migration; never run two live sessions on it

- **Status:** accepted
- **Source:** SPEC §4.5, §8 · commit `0442488`

ADR-0006 keys the conversation on the person and the audio stream on the
device, which is what makes migration resume the same log. It does not say what
becomes of the session on the device the person left, and the answer is not
"nothing": every session runs its own ~20 s silence backstop off its own
activity channel, so a session left behind writes `session_closed` into a
conversation someone is still talking to, and `journal.Reduce` folds that into
`Open = false` while later events keep arriving. A conversation therefore has
**exactly one live session**, and a resumed wake takes it from the incumbent.

The live session is indexed on `Conversations`, beside the person index,
because that is the only state per-satellite supervisors share — a `Supervisor`
carries one `Speaker`, so it is implicitly one satellite, and a shared
`Conversations` is already what makes cross-device resume work at all. The
handoff is serialised per **person**, not per conversation: two satellites in
earshot of one wake word is ordinary, and conversation ids are never reused, so
a table keyed on them would grow for the life of the process.

A migration is recorded as a `session_closed` with reason `migrated` followed
by a resumed `session_opened`, in that order. The session did end; the
conversation did not, and the next open says so.

```go
import "github.com/teagan42/chorus/internal/journal"

// Close before open, in one log: a reducer folding them the other way round
// ends on the session that lost the conversation.
func handoff(to string) []journal.Record {
	return []journal.Record{
		{Kind: journal.KindSessionClosed, Fields: map[string]string{
			"reason": "migrated", "satellite": "kitchen",
		}},
		{Kind: journal.KindSessionOpened, Fields: map[string]string{
			"satellite": to, "resumed": "true",
		}},
	}
}
```

Speech the old device never played is discarded with reason `migrated`, so it
stays on the unheard side of replay: the person walked away from it, and the
model must not believe they heard it (SPEC §4.4). The conversation keeps it as
context, because the log is the same log.

Two unretrofittable fields land with this (SPEC §15.4). `session_opened` records
`resumed`, so a reducer can tell a migration from a fresh conversation, and
`session_closed` records `satellite`, so a close pairs with its open. Neither
can be added to logs already written.

## Alternatives rejected

A dedicated `session_migrated` kind was rejected as a second way to say what
the close and the open already say, at the cost of a reducer case and a
`Handled` entry on every consumer. Rejecting the second wake while a session is
live was rejected because it breaks §4.5 outright: the person would get silence
on the new device until the old one timed out.

Letting both sessions live and reconciling their speech was rejected for phase
1. It is where the design ends up — the conversation is the actor and devices
attach to it — but it needs `Speaker` resolved per utterance rather than per
supervisor, and the handoff is a strict subset of that work.

## Forecloses

Two devices playing one conversation's speech at once. A session now ends when
another claims the conversation, so a "follow me into the next room while it
keeps talking" behaviour needs the multi-stream shape above, not a flag.
