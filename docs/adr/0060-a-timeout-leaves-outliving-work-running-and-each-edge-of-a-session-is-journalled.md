# 0060. A timeout leaves outliving work running, and each edge of a session is journalled

- **Status:** accepted
- **Source:** SPEC §4.3, §4.4, §4.5, §5, §6, §8, §9.3, §14 · ADR-0004, ADR-0031, ADR-0049 · PR #79, its commits
  `fix(session): a timeout leaves detach and uninterruptible work running`,
  `fix(session): log a journal write that fails with no turn to report it`,
  `fix(session): record the cut of what was playing before a session closes`,
  `fix(session): keep the conversation in use alive, whoever spoke in it`,
  `fix(listen): reject a wake whose first words could not be decoded`,
  `feat(session): record a barge-in the speaker stage never judged`,
  `fix(ollama): offer the model only tools something runs`,
  `fix(session): run a guest-scoped tool in guest context for everyone`,
  `fix(listen): keep a long overlap's audio once, not once per partial`,
  `fix(reviewui): re-run under the tools chorusd offers`

Refines ADR-0031 and ADR-0049, which it does not supersede. An audit of
`chorusd` found nine places where the session did something other than what
its own policy or log said. Each is fixed where it was, and the decisions
the fixes needed are recorded here together because none needed a record of
its own.

## Decisions

**A timeout is not an interruption** (SPEC §4.4). The model is told
`timed_out` and the turn goes on, but the deadline no longer cancels the
call's context. A `detach` or `uninterruptible` call runs to its end, and
its result is journalled as `detached`, as a detached call's late result
already was. Only a `cancel` call is cancelled at its deadline. Before, the
timeout's deferred cancel killed an unlocking door mid-way.

**A journal write with no turn to report it is logged** (SPEC §8). The
backstop, `end_session` and the close after an announcement record outside
any turn, so their errors were kept for a turn that never read them. They
are now logged where they happen; inside a turn they are still returned.

**A close waits for the cut** (SPEC §4.4, §4.5). A migration stops the
playing speech and then closes the session. The close now waits for the
speaker to settle, so `speech_truncated` is journalled before
`session_closed` and the replayed conversation reads in the order it
happened.

**Activity keeps the conversation alive, not the speaker's** (SPEC §4.5).
`Conversations.Touch` takes the conversation and extends every person keyed
to it. After an attribution flip it used to extend the newcomer's own,
letting the conversation in use expire mid-session. This lifts ADR-0049's
foreclosed note on `Touch`; a guest's conversation is keyed to nobody and
still touches nothing.

**A wake nothing could decode is rejected** (SPEC §9.3). A final decode
that fails on the first utterance now writes `wake_rejected` with reason
`transcription_failed`, so the false-wake corpus sees it. It is no hard
negative: the wake word may well have been said. A link that closes
mid-decode writes nothing, since that is the session ending, not the wake.

**A detection says when the speaker stage did not run** (ADR-0031's
follow-up). `barge_in_detected` carries `speaker_stage_skipped` when the
gate runs without an identifier, so the tuning corpus can tell a voice that
passed the stage from one never checked against it.

**The model is offered what this build can run** (SPEC §6, §14). Every
tool `chorusd` has an executor for is offered, configured or not; a
declared tool with nothing behind it is marked `deferred` in
`schema/tool.cue` and offered to nobody. So `ha_*` stays offered with Home
Assistant off and answers `not_implemented`, a result the model reasons
about (SPEC §7), while `media_search` is not offered until something runs
it. The offered set, and so the tool-schema version, is a property of the
build rather than of one house's configuration, so Replay offers what
`chorusd` did.

```go
import "github.com/teagan42/chorus/internal/registry"

// What chorusd and Replay offer the model: every declared tool but a deferred one.
func offered() map[string]registry.ToolSpec { return registry.Offered() }
```

**A guest-scoped tool runs as a guest for everyone** (SPEC §5). SPEC names
the `guest` scope without saying what it does differently from
`household`. SPEC §5 gates only person-scoped tools for a guest, so
`household` already means anyone; the conservative reading of `guest` is
that the tool never acts for the speaker, even a known one. Its caller has
no person, so it can read and keep nothing personal. No shipped tool uses
the scope yet.

**A candidate names its utterance's blob** (SPEC §4.3). Each partial judged
during playback stored the whole utterance so far, so an overlap of n
partials kept O(n²) audio. A candidate now refers to the utterance's one
blob with `audio_frames`, the prefix the gate judged. The blob is written
when the utterance ends, or as the judged prefix when it is never kept
whole, as ADR-0049's refused voice is not. Harvest pairs and the review UI
bound the barge-in clip at that prefix.

## Alternatives rejected

- **Hide `ha_*` when Home Assistant is off.** The offered set would then
  differ by house, and Replay, which has no house configuration, would
  offer a different set from the turn it replays and so fingerprint a
  different tool schema.
- **Read `guest` as `household`.** It makes the scope a synonym. Running
  the tool for nobody is the reading that cannot leak a person's data if a
  tool is later marked guest on purpose.
- **Keep one blob per candidate, but trimmed to the new audio.** Every
  reader would have to stitch candidates back together to hear what the
  gate heard; a prefix of one blob is already that.

## Forecloses

- **A `detach` or `uninterruptible` call has no deadline of its own.** One
  that never returns holds its goroutine until the session ends.
- **A candidate's audio exists only once its utterance ends.** A reader
  following the log live finds the blob missing until then, as with any
  utterance's audio, since a blob is visible only once committed.
- **Logs from before this change** carry no `audio_frames`; their
  candidates' blobs are their own, and readers treat the missing field as
  "all of it".
