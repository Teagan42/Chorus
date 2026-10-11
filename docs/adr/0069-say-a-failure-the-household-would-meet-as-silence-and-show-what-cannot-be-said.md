# 0069. Say a failure the household would meet as silence, and show on the ring what cannot be said

- **Status:** accepted · refines [ADR-0051](0051-a-failed-model-or-voice-is-said-and-kept-and-never-a-barge-in.md) and [ADR-0056](0056-an-interjection-pauses-the-voice-and-the-session-drives-the-ring.md)
- **Source:** SPEC §3.3.1, §7, §8, §9.3 · ADR-0007, ADR-0030, ADR-0049, ADR-0051, ADR-0056, ADR-0065 · commits
  `feat(schema): name the ear's apology and a failed audio write`,
  `feat(session): muted and error ring states, composed by a panel`,
  `feat(session): say the ear's canned line and show what is down`,
  `feat(listen): apologise when the ear fails, and show mute and faults`,
  `feat(tools): render the canned lines into fallback audio with Kokoro`,
  `feat(satellite): say canned lines from built-in audio until rendered`,
  `feat(chorusd): show the mute and faults on each satellite's ring`,
  `feat(esphome): Muted and Error effects on the Satellite1's LED ring`

ADR-0051 made a failed model or voice audible. Every other failure still
ended in silence, and the ring could not say why:

- **STT down on a wake.** `complete` in `internal/listen/listen.go`
  journalled `wake_rejected` with `transcription_failed`, and Alan heard
  nothing after "Hey Eddie, turn on the porch light".
- **STT down mid-session.** The utterance was logged as a warning and
  dropped. ADR-0030 had said why: the journal has no kind for "heard and
  never transcribed".
- **A blob write that failed.** The utterance was dropped, so the house
  ignored a question it had decoded. A rejected wake whose audio failed was
  not journalled at all. The disk guard (ADR-0065) declines writes below
  its floor without failing them, so a full disk is already silent by design;
  an I/O error was not.
- **Postgres down.** `Supervisor.Open` failed at `session_opened`, and the
  listener logged a warning.
- **Kokoro down at startup.** `rehearse` in `cmd/chorusd/run.go` retried
  every 30 s, and until it succeeded the voice's apology was silent too.
- **The ring** knew Listening, Thinking, Speaking and off. The device's
  `mute` frame reached the listener and nothing showed it.

```go
import "github.com/teagan42/chorus/internal/session"

// What Alan hears when the ear fails on him, instead of nothing.
var ear = session.DefaultCanned.Ear

// What the kitchen's ring shows while Bob's mute is on, whatever else is true.
var muted = session.RingMuted
```

## The ear gets a canned line

A third line, `Canned.Ear`: "Sorry, my hearing isn't working right now. Try
again in a minute." It is a speak call marked `canned`, like the other two,
and also `unheard`.

- **In a session**, `Session.Unheard` says it in the utterance's turn, in
  order behind the one before. Nothing is transcribed: no event can hold
  words nothing decoded, and adding a kind means teaching the review UI and
  the harvester a kind first. The line marked `unheard` is the record that
  someone spoke.
- **On a wake**, the rejection stays `transcription_failed`. Nothing
  confirmed the wake, so no conversation opens. But the person is owed an
  answer, so the listener asks the speaker stage first. A voice stage three
  would have let in is told. That covers a household member, or anyone when
  nobody is enrolled or speaker ID is down. `Supervisor.Misheard` writes the
  rejection to the device's log with the new `canned_call_id`, then says the
  line there, with no session. The television's wake stays silent: an
  apology to it is a false accept the household hears.

SPEC §9.3 kept every rejection imperceptible. This is the one exception,
and the SPEC now says so.

## The journal down is shown, never said

The log is the runtime (ADR-0007). A canned line is a speak call, and a
speak call the journal refuses would be a word the house said that no
reader, replay or corpus can find. So with Postgres down nothing is said.
Each satellite's ring shows the error instead. Every write a session or a
listener makes reports to the ring whether the journal took it: a refused
`session_opened`, a refused rejection, any refused event. The next write that
lands clears it.

## A failed audio write is a gone clip, not a lost turn

The words were decoded, so the turn goes on. The utterance is journalled
under the ref it would have had, and `audio_dropped` follows at once with
the new reason `write_failed`. That is the event ADR-0065 added so readers
show a clip as gone rather than fail on it. A rejected wake is journalled
the same way, with its own `write_failed`.

## The canned lines exist before they are needed

They are rendered at startup as before, and until that succeeds they are
said from 16 kHz PCM built into the binary. `task canned:voice` produces it.
`internal/tools/cannedvoice` lists every segment the satellite cuts a canned
line into, and `internal/tools/householdvoice/canned.py` renders each one
with Kokoro v1.0 over kokoro-onnx, in `af_heart`, chorusd's default voice.
The script reuses the household fixture's renderer. The files live in
`internal/satellite/fallback/`, named by a hash of the segment. The atomic
check counts them as generated, so they ship in `chore(gen)` commits.
`internal/satellite`'s tests fail when a canned line has a segment with no
file, or a file no segment names.

`satellite.Canned` answers a segment from its own rendering first. Next it
uses the built-in audio, and only then the synthesiser. The built-in audio
comes before the synthesiser because a Kokoro that hangs rather than refuses
would hold the apology until the client gave up.

## The ring shows the mute and what is down

`RingMuted` and `RingError` join the session's states. A `session.Panel` per
satellite, which its sessions and its listener share, decides what the ring
shows:

1. **Muted**, while the device reports its hardware or software mute. The mute
   is the household's privacy control (CONTRIBUTING §7), so nothing hides it.
2. **The session's state**, while one is open. A failure in a session is
   already said, so the session's state is what matters then.
3. **Error**, while any fault is outstanding: the ear, the journal, the model
   or the voice failed. Each fault holds until that dependency next answers.
   For the ear that is a decode, for the journal a write, for the model a
   finished turn, and for the voice a line the model chose playing out. A
   canned line playing proves nothing, since it may be the built-in audio.
4. Off.

`esphome/satellite1.yaml` gives the ring a steady red `Muted` and a blinking
amber `Error`. chorusd still requires the three session effects before it
drives a ring at all, as ADR-0056 says. `Muted` and `Error` are optional per
effect: a ring missing one is turned off in that state, so a ring on older
firmware works as it did. The one difference is that it goes dark while muted
mid-session, because "listening" would be untrue.

## Alternatives rejected

- **Refuse readiness until the lines are rendered.** The house would be mute
  for as long as Kokoro is, though with the audio built in every canned line
  can be said without it. The health wiring is also not this change's to
  own.
- **Say the journal-down apology anyway, unjournalled.** It is the one
  speech the log could never account for, and ADR-0007 is unretrofittable.
- **Open a session for a wake the ear failed on, to apologise in.** A
  `session_opened` beside a `wake_rejected` contradicts it, and the
  conversation it opens is keyed on a person nothing confirmed.
- **A new event kind for an utterance the ear failed on.** It is the honest
  shape. But the review UI requires a tone for every kind, the harvester
  folds every kind, and neither was this change's to edit. The marked
  canned line records the fact meanwhile.
- **An error state that outranks a live session.** It would hide what the
  session is doing, and the session already says its failures out loud.
- **A timed error flash instead of a held fault.** The ring would say nothing
  between attempts, and a timer is one more thing to inject.

## Forecloses

- **A fault clears only when its dependency is next used.** A ring can stay
  amber after Postgres or the sidecar is back until someone speaks to that
  satellite. Each ring shows what its own satellite saw.
- **The built-in audio is `af_heart`.** A household that configures another
  voice hears the apologies in `af_heart` until the startup render succeeds.
  Rewording a canned line means running `task canned:voice`, and the tests
  say so.
- **The harvester recognises a canned line by the failure event that names
  it.** The mid-session ear line has none. A pair cut from it needs a
  barge-in, which needs STT partials, which just failed, so none is expected.
  But the harvester should learn `unheard` the next time it is touched.
- **The audio of an utterance the ear failed on is not kept**, beyond any
  prefix a barge-in candidate already named.
- **A device log can now hold speech:** the ear's apology after a rejection.
  The reducer folds it as it would anywhere, and nothing replays device logs
  (ADR-0065).
- **A failed write of a judged prefix is still only logged**, as before.
- **A satellite whose audio link cannot reach chorusd shows nothing.** A
  firmware-side "orchestrator unreachable" effect driven by the bridge's
  connection state needs a change in `chorus_bridge`, which was not this
  change's to make.
