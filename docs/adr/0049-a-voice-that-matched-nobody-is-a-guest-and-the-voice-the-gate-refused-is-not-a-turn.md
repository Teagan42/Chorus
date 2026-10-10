# 0049. A voice that matched nobody is a guest, and the voice the gate refused is not a turn

- **Status:** accepted
- **Source:** SPEC §4.3, §5 · ADR-0004, ADR-0031, ADR-0040

Refines ADR-0040 and ADR-0004, which it does not supersede. ADR-0040's
Forecloses section admitted the speaker was sticky: a guest who chimed into
Teagan's conversation without matching anyone was still Teagan, was told her
memories, and could `forget` them as her. And the barge-in gate only decided
whether speech stopped: the television it refused was heard as a turn once
it paused, as whoever spoke before it.

**How a voice matched is recorded, and a voice that matched nobody is a
guest.** `utterance_transcribed` carries `speaker_match`, the reason
identity resolved the voice as it did. `below_threshold` and `ambiguous`
mean the voice was judged and is nobody the household enrolled, so the turn
is a guest's whoever spoke before. SPEC §5 already gives an ambiguous voice
guest context, because guessing between two people hands one person's
context to the other. The session and the reducer attribute through one
function, so a replay asks as the turn ran:

```go
import "github.com/teagan42/chorus/internal/journal"

// A friend over for dinner chimes into Alice's conversation and matches
// nobody: the turn is a guest's, not Alice's.
func friendsTurn() string { return journal.Attribute("alice", "", "below_threshold") }
```

A voice nothing judged keeps the current speaker. With no identifier, or an
embedder that failed for one utterance, there is no evidence of anyone else
(ADR-0031), and `nobody_enrolled` cannot tell people apart at all. A log from
before `speaker_match` was recorded has none, so it replays exactly as it
always did.

**A guest's turn is told nothing, and the log says so.** `memory_recalled` is
recorded when what a turn is told changes. A guest following someone who was
told something records one with an empty `person` and no memories, which is
why `person` is no longer required. Without it the reducer would carry the
previous person's memories into the guest's ask, and so would the harvester
and Replay, which read the same log. Person-scoped tools already refuse a
guest (ADR-0040), so `forget` and `remember` are refused before they run.

**The voice the gate refused is not a turn.** An utterance that talked over
speech, never stopped it, and is judged on its whole audio to be a voice the
household does not know is dropped when it ends. It is the television SPEC
§4.3 has the speaker stage reject, and its rejected candidates are already
in the log with their audio as the tuning corpus. Answering it a pause later
would undo the refusal.

The drop is narrow on purpose. A household voice refused for a short partial
or a quiet start is still heard: "thanks" over the end of an answer was not
an interruption, but it was said. The final judgment uses the whole
utterance, so a household member whose first half-second was too short to
identify is not dropped. And a voice nothing judged is never dropped, since
the television and a guest cannot be told apart then and SPEC §5 answers the
guest.

## Alternatives rejected

- **Clear the recalled memories in the reducer on a guest's utterance.** It
  works for replay, but the harvester keeps its own copy of the last
  `memory_recalled`, so it would have to repeat the rule. A recorded empty
  recall is one fact every reader of the log already handles.
- **Drop any utterance the gate refused.** The gate refuses a one-word
  partial and a quiet start too. Those are about whether speech stops, not
  about who spoke, and dropping them would lose what a household member said.
- **Treat an empty `speaker_id` as a guest.** It cannot tell "matched
  nobody" from "nothing judged", and it would rewrite how every existing log
  replays.

## Forecloses

- **A guest who talks over speech is not heard.** The gate cannot tell a
  dinner guest from the television mid-sentence. The guest is heard once
  speech ends, or by waiting for it.
- **The dropped utterance has no event of its own.** Its candidates'
  `barge_in_rejected` rows carry the audio the gate judged, but not the
  final utterance's audio or text.
- **`Touch` after a flip still extends the newcomer's own conversation**, and
  a guest touches nothing. Unchanged here (audit 2026-10-10, Broken 8).
